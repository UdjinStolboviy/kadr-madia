// Package alerts is the built-in alerting: it evaluates a few operational
// rules once a minute and sends notifications to Telegram (OPERATIONS.md §9).
package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
)

// Notifier delivers a message.
type Notifier interface {
	Notify(ctx context.Context, text string) error
}

// Telegram sends messages through the Bot API.
type Telegram struct {
	Token, ChatID string
	Client        *http.Client
	BaseURL       string // default https://api.telegram.org
}

// Notify implements Notifier.
func (t *Telegram) Notify(ctx context.Context, text string) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	c := t.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	form := url.Values{"chat_id": {t.ChatID}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s", redact(err.Error(), t.Token))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		var e struct{ Description string }
		_ = json.Unmarshal(b, &e)
		return fmt.Errorf("telegram: HTTP %d %s", resp.StatusCode, e.Description)
	}
	return nil
}

func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

// Thresholds of the built-in rules.
type Thresholds struct {
	VideoQueue    int64         // queued video jobs above this alert (default 10)
	OldestQueued  time.Duration // oldest queued job older than this (default 15m)
	DeadWindow    time.Duration // window for dead jobs (default 15m)
	DeadJobs      int64         // dead jobs within the window (default 3)
	DiskFreeBytes uint64        // free tmp disk below this (default 2 GiB)
	Cooldown      time.Duration // re-alert interval per rule (default 30m)
	CheckEvery    time.Duration // default 1m
	DiskFree      func() (uint64, error)
	Now           func() time.Time
}

func (t *Thresholds) defaults() {
	if t.VideoQueue <= 0 {
		t.VideoQueue = 10
	}
	if t.OldestQueued <= 0 {
		t.OldestQueued = 15 * time.Minute
	}
	if t.DeadWindow <= 0 {
		t.DeadWindow = 15 * time.Minute
	}
	if t.DeadJobs <= 0 {
		t.DeadJobs = 3
	}
	if t.DiskFreeBytes == 0 {
		t.DiskFreeBytes = 2 << 30
	}
	if t.Cooldown <= 0 {
		t.Cooldown = 30 * time.Minute
	}
	if t.CheckEvery <= 0 {
		t.CheckEvery = time.Minute
	}
	if t.Now == nil {
		t.Now = time.Now
	}
}

// Monitor evaluates the rules.
type Monitor struct {
	store *db.Store
	n     Notifier
	th    Thresholds
	log   *slog.Logger
	host  string

	mu   sync.Mutex
	last map[string]time.Time // rule → last alert
	open map[string]bool      // rules currently firing
}

// New creates a monitor. host is shown in messages.
func New(store *db.Store, n Notifier, th Thresholds, host string, log *slog.Logger) *Monitor {
	th.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{store: store, n: n, th: th, log: log, host: host, last: map[string]time.Time{}, open: map[string]bool{}}
}

// Run evaluates until ctx is done.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.th.CheckEvery)
	defer t.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check evaluates all rules once, sending alerts for newly firing rules (and
// a "resolved" message when a rule stops firing).
func (m *Monitor) Check(ctx context.Context) {
	now := m.th.Now()
	firing := map[string]string{}

	if n, err := m.store.CountJobs(ctx, queue.PoolVideo, db.JobQueued); err == nil && n > m.th.VideoQueue {
		firing["video_queue"] = fmt.Sprintf("video queue has %d waiting jobs (limit %d)", n, m.th.VideoQueue)
	}
	for _, pool := range []string{queue.PoolImage, queue.PoolVideo} {
		if age, err := m.store.OldestQueuedAge(ctx, pool, now); err == nil && age > m.th.OldestQueued {
			firing["oldest_"+pool] = fmt.Sprintf("oldest %s job has waited %s", pool, age.Round(time.Second))
		}
		if n, err := m.store.CountFinishedJobsSince(ctx, pool, db.JobDead, now.Add(-m.th.DeadWindow)); err == nil && n >= m.th.DeadJobs {
			firing["dead_"+pool] = fmt.Sprintf("%d %s jobs died in the last %s", n, pool, m.th.DeadWindow)
		}
	}
	if m.th.DiskFree != nil {
		if free, err := m.th.DiskFree(); err == nil && free < m.th.DiskFreeBytes {
			firing["disk"] = fmt.Sprintf("only %d MiB free on the tmp disk", free>>20)
		}
	}
	m.dispatch(ctx, now, firing)
}

func (m *Monitor) dispatch(ctx context.Context, now time.Time, firing map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for rule, msg := range firing {
		if m.open[rule] && now.Sub(m.last[rule]) < m.th.Cooldown {
			continue
		}
		if err := m.n.Notify(ctx, fmt.Sprintf("⚠️ Kadr %s: %s", m.host, msg)); err != nil {
			m.log.Warn("alert delivery failed", "rule", rule, "err", err)
			continue // retried on the next check
		}
		m.open[rule], m.last[rule] = true, now
	}
	for rule := range m.open {
		if _, ok := firing[rule]; ok || !m.open[rule] {
			continue
		}
		if err := m.n.Notify(ctx, fmt.Sprintf("✅ Kadr %s: %s recovered", m.host, rule)); err == nil {
			delete(m.open, rule)
			delete(m.last, rule)
		}
	}
}
