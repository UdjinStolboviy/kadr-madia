package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// RetrySchedule is the delay after each failed attempt (docs/API.md): 10 s,
// 30 s, 2 min, 10 min, 30 min, 1 h, 3 h, 6 h, 12 h (~24 h in total). The
// delivery is dead after the attempt that follows the last delay, i.e.
// after len(RetrySchedule)+1 attempts.
var RetrySchedule = []time.Duration{
	10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
}

const (
	// RequestTimeout bounds one delivery attempt.
	RequestTimeout = 10 * time.Second
	// deliveryLease must exceed RequestTimeout; an expired lease (crash
	// mid-request) makes the delivery claimable again.
	deliveryLease = 30 * time.Second
	maxErrorLen   = 500
)

// Config configures a Dispatcher.
type Config struct {
	Store   *db.Store
	Keyring *crypto.Keyring
	Logger  *slog.Logger
	Metrics *metrics.Set
	// Workers is the number of concurrent deliveries (default 4).
	Workers int
	// HTTPClient overrides the client (its Timeout is forced to RequestTimeout
	// when zero). Redirects are never followed.
	HTTPClient *http.Client
	// Schedule overrides RetrySchedule (tests).
	Schedule []time.Duration
	// PollInterval is the idle re-check interval (default 1 s).
	PollInterval time.Duration
	Now          func() time.Time
}

// Dispatcher delivers pending webhook deliveries.
type Dispatcher struct {
	cfg  Config
	log  *slog.Logger
	wake chan struct{}

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	runCtx   context.Context
	cancel   context.CancelFunc
}

// New builds a dispatcher.
func New(cfg Config) *Dispatcher {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.Schedule == nil {
		cfg.Schedule = RetrySchedule
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	client := *cfg.HTTPClient
	if client.Timeout == 0 {
		client.Timeout = RequestTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.HTTPClient = &client
	d := &Dispatcher{cfg: cfg, log: cfg.Logger, wake: make(chan struct{}, 1), stop: make(chan struct{})}
	d.runCtx, d.cancel = context.WithCancel(context.Background())
	return d
}

// MaxAttempts is the total number of attempts before a delivery is dead.
func (d *Dispatcher) MaxAttempts() int { return len(d.cfg.Schedule) + 1 }

// Notify wakes an idle worker (call after committing new deliveries).
func (d *Dispatcher) Notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Start launches the delivery workers.
func (d *Dispatcher) Start(ctx context.Context) {
	d.runCtx, d.cancel = context.WithCancel(context.WithoutCancel(ctx))
	for range d.cfg.Workers {
		d.wg.Add(1)
		go d.worker()
	}
	d.log.Info("webhook dispatcher started", "workers", d.cfg.Workers)
}

// Shutdown stops claiming and waits up to timeout for in-flight requests.
// Deliveries interrupted afterwards are retried after their lease expires.
func (d *Dispatcher) Shutdown(timeout time.Duration) {
	d.stopOnce.Do(func() { close(d.stop) })
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		d.cancel()
		<-done
	}
	d.cancel()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		if d.ProcessOne(d.runCtx) {
			continue
		}
		timer.Reset(d.cfg.PollInterval)
		select {
		case <-d.stop:
			return
		case <-d.wake:
		case <-timer.C:
		}
	}
}

// ProcessOne claims and attempts one due delivery. It reports whether a
// delivery was processed.
func (d *Dispatcher) ProcessOne(ctx context.Context) bool {
	now := d.cfg.Now()
	del, err := d.cfg.Store.ClaimDelivery(ctx, "", now, now.Add(deliveryLease))
	if errors.Is(err, db.ErrNotFound) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("claim webhook delivery", "err", err)
		}
		return false
	}
	d.attempt(ctx, del)
	return true
}

// DeliverNow immediately attempts a pending delivery (admin "test" and
// manual retry) and returns its updated state.
func (d *Dispatcher) DeliverNow(ctx context.Context, deliveryID string) (*db.Delivery, error) {
	now := d.cfg.Now()
	del, err := d.cfg.Store.ClaimDelivery(ctx, deliveryID, now, now.Add(deliveryLease))
	if err != nil {
		return nil, err
	}
	d.attempt(ctx, del)
	return d.cfg.Store.GetDelivery(ctx, deliveryID)
}

func (d *Dispatcher) attempt(ctx context.Context, del *db.Delivery) {
	log := d.log.With("delivery_id", del.ID, "event_id", del.EventID, "endpoint_id", del.EndpointID, "attempt", del.Attempts)
	start := time.Now()
	code, sendErr := d.send(ctx, del)
	dur := time.Since(start)
	now := d.cfg.Now()

	res := db.DeliveryResult{DurationMs: dur.Milliseconds(), At: now, NextAttemptAt: now}
	if code > 0 {
		c := int64(code)
		res.StatusCode = &c
	}
	switch {
	case sendErr == nil:
		res.Status = db.DeliveryDelivered
	case del.Attempts >= d.MaxAttempts():
		res.Status = db.DeliveryDead
	default:
		res.Status = db.DeliveryPending
		res.NextAttemptAt = now.Add(d.cfg.Schedule[del.Attempts-1])
	}
	if sendErr != nil {
		msg := sendErr.Error()
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen]
		}
		res.Error = &msg
	}
	octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := d.cfg.Store.FinishDeliveryAttempt(octx, del.ID, res); err != nil {
		log.Error("record webhook attempt", "err", err)
	}
	label := res.Status
	if label == db.DeliveryPending {
		label = "failed"
	}
	if d.cfg.Metrics != nil {
		d.cfg.Metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_webhook_deliveries_total{status=%q}`, label)).Inc()
	}
	switch res.Status {
	case db.DeliveryDelivered:
		log.Debug("webhook delivered", "status_code", code, "duration_ms", res.DurationMs)
	case db.DeliveryDead:
		log.Warn("webhook delivery dead", "status_code", code, "err", sendErr)
	default:
		log.Info("webhook delivery failed, will retry", "status_code", code, "err", sendErr, "next_attempt_at", res.NextAttemptAt)
	}
}

// send performs one signed POST. It returns the HTTP status (0 when no
// response) and an error unless the receiver answered 2xx.
func (d *Dispatcher) send(ctx context.Context, del *db.Delivery) (int, error) {
	ev, err := d.cfg.Store.GetEvent(ctx, del.EventID)
	if err != nil {
		return 0, fmt.Errorf("load event: %w", err)
	}
	ep, err := d.cfg.Store.GetWebhookEndpoint(ctx, del.EndpointID)
	if err != nil {
		return 0, fmt.Errorf("load endpoint: %w", err)
	}
	if ep.DisabledAt != nil {
		return 0, errors.New("endpoint is disabled")
	}
	secret, err := DecryptSecret(d.cfg.Keyring, ep.ID, ep.SecretEnc)
	if err != nil {
		return 0, err
	}
	ts := d.cfg.Now().Unix()
	sig, err := Sign(secret, ev.ID, ts, ev.Payload)
	if err != nil {
		return 0, err
	}
	rctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, ep.URL, bytes.NewReader(ev.Payload))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Kadr-Webhooks/1")
	req.Header.Set("webhook-id", ev.ID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("webhook-signature", sig)
	resp, err := d.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST failed: %w", err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("receiver returned %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return resp.StatusCode, nil
}
