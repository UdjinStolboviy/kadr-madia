package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
)

type rec struct{ msgs []string }

func (r *rec) Notify(_ context.Context, t string) error { r.msgs = append(r.msgs, t); return nil }

func TestRulesCooldownAndRecovery(t *testing.T) {
	ctx := context.Background()
	store := dbtest.Open(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 12; i++ {
		_, _, err := store.InsertJob(ctx, &db.Job{ID: db.NewID(db.PrefixJob), Kind: "process_video", Pool: queue.PoolVideo, Payload: []byte(`{}`),
			Status: db.JobQueued, MaxAttempts: 3, RunAt: now.Add(-20 * time.Minute), CreatedAt: now.Add(-20 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := &rec{}
	clock := now
	free := uint64(10 << 30)
	m := New(store, r, Thresholds{Now: func() time.Time { return clock }, DiskFree: func() (uint64, error) { return free, nil }}, "test", nil)
	m.Check(ctx)
	if len(r.msgs) != 2 { // queue depth + oldest waiting
		t.Fatalf("msgs: %v", r.msgs)
	}
	clock = clock.Add(5 * time.Minute)
	m.Check(ctx)
	if len(r.msgs) != 2 {
		t.Fatalf("cooldown not applied: %v", r.msgs)
	}
	free = 1 << 20
	m.Check(ctx)
	if len(r.msgs) != 3 || !strings.Contains(r.msgs[2], "free") {
		t.Fatalf("disk: %v", r.msgs)
	}
	// Drain the queue: rules resolve.
	for {
		j, err := store.ClaimJob(ctx, queue.PoolVideo, "w", clock, clock.Add(time.Minute))
		if err != nil || j == nil {
			break
		}
		_, _ = store.SucceedJob(ctx, j.ID, "w", clock)
	}
	free = 10 << 30
	m.Check(ctx)
	rec := 0
	for _, s := range r.msgs {
		if strings.Contains(s, "recovered") {
			rec++
		}
	}
	if rec != 3 {
		t.Fatalf("recovered messages: %d in %v", rec, r.msgs)
	}
}

func TestTelegramRedactsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" || r.FormValue("chat_id") != "42" {
			t.Errorf("bad request %s %v", r.URL.Path, r.Form)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	tg := &Telegram{Token: "TOKEN", ChatID: "42", BaseURL: srv.URL}
	if err := tg.Notify(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	err := tg.Notify(context.Background(), "hi")
	if err == nil || strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}
