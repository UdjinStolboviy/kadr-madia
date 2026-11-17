package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
)

// Test vector from the Standard Webhooks reference libraries
// (github.com/standard-webhooks/standard-webhooks).
func TestSignStandardWebhooksVector(t *testing.T) {
	const (
		secret  = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
		msgID   = "msg_p5jXN8AQM9LWM0D4loKWxJek"
		ts      = int64(1614265330)
		payload = `{"test": 2432232314}`
		want    = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
	)
	got, err := Sign(secret, msgID, ts, []byte(payload))
	if err != nil || got != want {
		t.Fatalf("Sign = %q, %v; want %q", got, err, want)
	}
	now := time.Unix(ts, 0)
	if err := Verify(secret, msgID, strconv.FormatInt(ts, 10), "v1,bogus "+want, []byte(payload), now, 0); err != nil {
		t.Fatalf("Verify (second signature matches): %v", err)
	}
	if err := Verify(secret, msgID, strconv.FormatInt(ts, 10), want, []byte(payload+" "), now, 0); err != ErrNoValidSignature {
		t.Fatalf("Verify tampered body: %v", err)
	}
	if err := Verify(secret, msgID, strconv.FormatInt(ts, 10), want, []byte(payload), now.Add(6*time.Minute), 0); err != ErrTimestamp {
		t.Fatalf("Verify stale timestamp: %v", err)
	}
	if err := Verify(secret, "", "1", want, nil, now, 0); err != ErrMissingHeaders {
		t.Fatalf("Verify missing headers: %v", err)
	}
}

// referenceSign re-implements the spec independently of Sign.
func referenceSign(secret, id string, ts int64, body []byte) string {
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "." + string(body)))
	return "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func TestGeneratedSecretsRoundTrip(t *testing.T) {
	s, err := GenerateSecret()
	if err != nil || !strings.HasPrefix(s, "whsec_") {
		t.Fatalf("secret %q %v", s, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "whsec_"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("secret key: %d %v", len(raw), err)
	}
	body := []byte(`{"type":"asset.ready"}`)
	got, _ := Sign(s, "evt_1", 1700000000, body)
	if got != referenceSign(s, "evt_1", 1700000000, body) {
		t.Fatal("Sign disagrees with the reference algorithm")
	}
	kr := newKeyring(t)
	enc, _ := EncryptSecret(kr, "whe_1", s)
	if back, err := DecryptSecret(kr, "whe_1", enc); err != nil || back != s {
		t.Fatalf("decrypt: %v", err)
	}
	if _, err := DecryptSecret(kr, "whe_2", enc); err == nil {
		t.Fatal("secret decrypted under another record id")
	}
}

func TestSubscribed(t *testing.T) {
	cases := []struct {
		f    []string
		typ  string
		want bool
	}{
		{[]string{"*"}, "asset.ready", true},
		{[]string{"asset.ready"}, "asset.ready", true},
		{[]string{"asset.*"}, "asset.failed", true},
		{[]string{"asset.*"}, "upload.expired", false},
		{[]string{"quota.warning"}, "asset.ready", false},
	}
	for _, c := range cases {
		if got := Subscribed(c.f, c.typ); got != c.want {
			t.Errorf("Subscribed(%v, %s) = %v", c.f, c.typ, got)
		}
	}
}

type fixture struct {
	store   *db.Store
	kr      *crypto.Keyring
	project string
	now     time.Time
	mu      sync.Mutex
}

func (f *fixture) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func newFixture(t *testing.T) *fixture {
	store := dbtest.Open(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := &db.Project{ID: db.NewID(db.PrefixProject), Slug: "acme", Name: "Acme", Environment: "live", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateProject(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return &fixture{store: store, kr: newKeyring(t), project: p.ID, now: now}
}

func (f *fixture) endpoint(t *testing.T, url string, events ...string) (string, string) {
	t.Helper()
	if len(events) == 0 {
		events = []string{"*"}
	}
	secret, _ := GenerateSecret()
	id := db.NewID(db.PrefixWebhookEndpoint)
	enc, _ := EncryptSecret(f.kr, id, secret)
	if err := f.store.CreateWebhookEndpoint(context.Background(), &db.WebhookEndpoint{ID: id, ProjectID: f.project, URL: url,
		SecretEnc: enc, Events: events, CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	return id, secret
}

func newKeyring(t *testing.T) *crypto.Keyring {
	key := make([]byte, crypto.KeySize)
	rand.Read(key)
	kr, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestEmitFansOutBySubscription(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	all, _ := f.endpoint(t, "http://a.example.com/hook")
	assets, _ := f.endpoint(t, "http://b.example.com/hook", "asset.*")
	f.endpoint(t, "http://c.example.com/hook", "quota.warning")

	var ev *db.Event
	err := f.store.InTx(ctx, func(tx *db.Store) error {
		var err error
		ev, err = Emit(ctx, tx, f.project, EventAssetReady, map[string]any{"asset": map[string]string{"id": "ast_1"}}, f.now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Type      string         `json:"type"`
		Timestamp string         `json:"timestamp"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(ev.Payload, &env); err != nil || env.Type != EventAssetReady || env.Timestamp == "" || env.Data["asset"] == nil {
		t.Fatalf("payload %s: %v", ev.Payload, err)
	}
	ds, _ := f.store.ListDeliveriesByEvent(ctx, ev.ID)
	got := map[string]bool{}
	for _, d := range ds {
		got[d.EndpointID] = true
		if d.Status != db.DeliveryPending {
			t.Fatalf("delivery status %s", d.Status)
		}
	}
	if len(ds) != 2 || !got[all] || !got[assets] {
		t.Fatalf("deliveries: %+v", ds)
	}
}

func TestDispatcherRetriesThenDelivers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var calls atomic.Int32
	var mu sync.Mutex
	var verified []error
	var secret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		verified = append(verified, Verify(secret, r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"),
			r.Header.Get("webhook-signature"), body, f.Now(), 0))
		mu.Unlock()
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		if calls.Add(1) <= 2 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	var epID string
	epID, secret = f.endpoint(t, srv.URL)

	var ev *db.Event
	f.store.InTx(ctx, func(tx *db.Store) error {
		var err error
		ev, err = Emit(ctx, tx, f.project, EventAssetReady, map[string]string{"id": "ast_1"}, f.now)
		return err
	})
	set := metrics.NewSet()
	d := New(Config{Store: f.store, Keyring: f.kr, Logger: quiet(), Metrics: set, Now: f.Now})

	if !d.ProcessOne(ctx) {
		t.Fatal("nothing processed")
	}
	del := deliveryOf(t, f, ev.ID)
	if del.Status != db.DeliveryPending || del.Attempts != 1 || *del.LastStatusCode != 503 || del.LastError == nil ||
		!del.NextAttemptAt.Equal(f.now.Add(10*time.Second)) || del.EndpointID != epID {
		t.Fatalf("after first failure: %+v", del)
	}
	if d.ProcessOne(ctx) {
		t.Fatal("retry ran before its schedule")
	}
	f.advance(10 * time.Second)
	d.ProcessOne(ctx)
	if del = deliveryOf(t, f, ev.ID); del.Attempts != 2 || !del.NextAttemptAt.Equal(f.now.Add(30*time.Second)) {
		t.Fatalf("after second failure: %+v", del)
	}
	f.advance(30 * time.Second)
	d.ProcessOne(ctx)
	if del = deliveryOf(t, f, ev.ID); del.Status != db.DeliveryDelivered || del.Attempts != 3 || del.DeliveredAt == nil || *del.LastStatusCode != 204 {
		t.Fatalf("after success: %+v", del)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, err := range verified {
		if err != nil {
			t.Fatalf("attempt %d signature: %v", i+1, err)
		}
	}
	var sb strings.Builder
	set.WritePrometheus(&sb)
	if !strings.Contains(sb.String(), `kadr_webhook_deliveries_total{status="failed"} 2`) ||
		!strings.Contains(sb.String(), `kadr_webhook_deliveries_total{status="delivered"} 1`) {
		t.Fatalf("metrics:\n%s", sb.String())
	}
}

func TestDispatcherDeadAfterLastAttemptAndManualRetry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	f.endpoint(t, srv.URL)
	var ev *db.Event
	f.store.InTx(ctx, func(tx *db.Store) error {
		var err error
		ev, err = Emit(ctx, tx, f.project, EventAssetFailed, map[string]string{"id": "ast_1"}, f.now)
		return err
	})
	d := New(Config{Store: f.store, Keyring: f.kr, Logger: quiet(), Now: f.Now})
	if d.MaxAttempts() != 10 {
		t.Fatalf("max attempts = %d", d.MaxAttempts())
	}
	var total time.Duration
	for i := range d.MaxAttempts() {
		if !d.ProcessOne(ctx) {
			t.Fatalf("attempt %d not due", i+1)
		}
		if i < len(RetrySchedule) {
			f.advance(RetrySchedule[i])
			total += RetrySchedule[i]
		}
	}
	if total < 22*time.Hour || total > 25*time.Hour {
		t.Fatalf("retry window = %v, want ~24h", total)
	}
	del := deliveryOf(t, f, ev.ID)
	if del.Status != db.DeliveryDead || del.Attempts != 10 {
		t.Fatalf("expected dead: %+v", del)
	}
	if d.ProcessOne(ctx) {
		t.Fatal("dead delivery was retried automatically")
	}
	fail.Store(false)
	if ok, _ := f.store.RetryDelivery(ctx, del.ID, f.Now()); !ok {
		t.Fatal("manual retry refused")
	}
	got, err := d.DeliverNow(ctx, del.ID)
	if err != nil || got.Status != db.DeliveryDelivered || got.Attempts != 1 {
		t.Fatalf("deliver now: %+v %v", got, err)
	}
}

func TestDispatcherWorkersDeliverAndTimeout(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	got := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("webhook-id")
	}))
	defer srv.Close()
	f.endpoint(t, srv.URL)
	// A receiver that never answers in time.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	f.endpoint(t, slow.URL)

	d := New(Config{Store: f.store, Keyring: f.kr, Logger: quiet(), Now: f.Now, PollInterval: 10 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 100 * time.Millisecond}})
	d.Start(ctx)
	defer d.Shutdown(time.Second)

	var ev *db.Event
	f.store.InTx(ctx, func(tx *db.Store) error {
		var err error
		ev, err = Emit(ctx, tx, f.project, EventAssetReady, map[string]string{"id": "ast_9"}, f.now)
		return err
	})
	d.Notify()
	select {
	case id := <-got:
		if id != ev.ID {
			t.Fatalf("webhook-id = %s, want %s", id, ev.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook not delivered by workers")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ds, _ := f.store.ListDeliveriesByEvent(ctx, ev.ID)
		var timedOut bool
		for _, del := range ds {
			if del.EndpointURL == slow.URL && del.Status == db.DeliveryPending && del.Attempts == 1 && del.LastStatusCode == nil {
				timedOut = true
			}
		}
		if timedOut {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("slow receiver was not timed out and rescheduled")
}

func deliveryOf(t *testing.T, f *fixture, eventID string) *db.Delivery {
	t.Helper()
	ds, err := f.store.ListDeliveriesByEvent(context.Background(), eventID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("deliveries of %s: %d %v", eventID, len(ds), err)
	}
	return ds[0]
}
