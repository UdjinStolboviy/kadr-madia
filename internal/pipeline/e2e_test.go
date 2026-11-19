package pipeline_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/file"
	"github.com/UdjinStolboviy/kadr-madia/internal/importer"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/public"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/fakeimage"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

const (
	publicBucket  = "kadr-public"
	privateBucket = "kadr-private"
	cdnBase       = "https://cdn.example.com"
)

var pepper = []byte("test-pepper-test-pepper-test-pepper")

// recDriver records the PutOptions of every object (gofakes3 does not keep
// Cache-Control).
type recDriver struct {
	storage.Driver
	mu   sync.Mutex
	puts map[string]storage.PutOptions
}

func (d *recDriver) Put(ctx context.Context, bucket, key string, body io.ReadSeeker, size int64, o storage.PutOptions) error {
	d.mu.Lock()
	d.puts[bucket+"/"+key] = o
	d.mu.Unlock()
	return d.Driver.Put(ctx, bucket, key, body, size, o)
}

func (d *recDriver) opts(bucket, key string) (storage.PutOptions, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	o, ok := d.puts[bucket+"/"+key]
	return o, ok
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t        *testing.T
	ctx      context.Context
	store    *db.Store
	svc      *pipeline.Service
	queue    *queue.Queue
	engine   *fakeimage.Engine
	driver   *recDriver
	api      *httptest.Server
	key      string
	project  *db.Project
	endpoint string
	clock    *clock
	metrics  *metrics.Set
	kr       *crypto.Keyring
}

func newEnv(t *testing.T, opts ...func(*pipeline.Config)) *env {
	t.Helper()
	ctx := context.Background()
	store := dbtest.Open(t)
	keyBytes := make([]byte, crypto.KeySize)
	rand.Read(keyBytes)
	kr, _ := crypto.NewKeyring(keyBytes)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clk := &clock{t: time.Now().UTC().Truncate(time.Millisecond)}

	backend := s3mem.New()
	backend.CreateBucket(publicBucket)
	backend.CreateBucket(privateBucket)
	fake := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(fake.Close)

	now := clk.Now()
	spID := db.NewID(db.PrefixStorageProfile)
	creds, _ := json.Marshal(map[string]string{"access_key_id": "AKIAEXAMPLE", "secret_access_key": "secret"})
	credsEnc, _ := kr.Encrypt(creds, []byte("storage_profile:"+spID))
	must(t, store.CreateStorageProfile(ctx, &db.StorageProfile{ID: spID, Name: "test", Provider: "r2", Endpoint: fake.URL,
		Region: "auto", PublicBucket: publicBucket, PrivateBucket: privateBucket, PublicBaseURL: cdnBase,
		CredentialsEnc: credsEnc, IsPlatform: true, CreatedAt: now, UpdatedAt: now}))
	p := &db.Project{ID: db.NewID(db.PrefixProject), Slug: "acme", Name: "Acme", Environment: "live", StorageProfileID: &spID,
		CreatedAt: now, UpdatedAt: now}
	must(t, store.CreateProject(ctx, p))
	for _, dp := range preset.DefaultPresets() {
		must(t, store.CreatePreset(ctx, &db.Preset{ID: db.NewID(db.PrefixPreset), ProjectID: p.ID, Slug: dp.Slug, Kind: dp.Kind,
			Description: dp.Description, CurrentVersion: 1, Params: dp.Params, CreatedAt: now}, "test"))
	}
	gen, _ := auth.GenerateAPIKey("live", pepper)
	must(t, store.CreateAPIKey(ctx, &db.APIKey{ID: db.NewID(db.PrefixAPIKey), ProjectID: p.ID, Name: "test", Prefix: gen.Prefix,
		SecretHash: gen.Hash, Scopes: auth.AllScopes, CreatedAt: now}))
	epID := db.NewID(db.PrefixWebhookEndpoint)
	secretEnc, _ := webhook.EncryptSecret(kr, epID, "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	must(t, store.CreateWebhookEndpoint(ctx, &db.WebhookEndpoint{ID: epID, ProjectID: p.ID, URL: "http://hooks.example.com/kadr",
		SecretEnc: secretEnc, Events: []string{"*"}, CreatedAt: now}))

	rec := &recDriver{puts: map[string]storage.PutOptions{}}
	q := queue.New(store, nil)
	q.SetBackoff(time.Millisecond, 5*time.Millisecond)
	set := metrics.NewSet()
	eng := fakeimage.New()
	pcfg := pipeline.Config{
		Store: store, Queue: q, Engine: eng, Video: videoEngine(), File: file.New(file.Config{}), MultipartThreshold: 8 << 20, PartSize: 5 << 20, EdgeBaseURL: "https://media.example.com", Fetcher: &importer.Fetcher{AllowAnyPort: true, AllowAddr: func(netip.Addr) bool { return true }}, Keyring: kr, Logger: log, Metrics: set, TmpDir: t.TempDir(), Now: clk.Now,
		NewDriver: func(c storage.Config) (storage.Driver, error) {
			d, err := storage.NewS3(c)
			if err != nil {
				return nil, err
			}
			rec.Driver = d
			return rec, nil
		},
	}
	for _, o := range opts {
		o(&pcfg)
	}
	svc := pipeline.New(pcfg)
	runner := queue.NewRunner(q, queue.RunnerConfig{Pools: map[string]int{queue.PoolImage: 2, queue.PoolVideo: 1, queue.PoolFile: 1, queue.PoolMaintenance: 1},
		Logger: log, Metrics: set, PollInterval: 10 * time.Millisecond})
	svc.Register(runner)
	runner.Start(ctx)
	t.Cleanup(func() { runner.Shutdown(2 * time.Second) })

	api := httptest.NewServer(public.Handler(public.Config{Store: store, Logger: log, Metrics: set, Pipeline: svc,
		TmpDir: t.TempDir(), Verifier: &auth.KeyVerifier{Store: store, Pepper: pepper}}))
	t.Cleanup(api.Close)
	return &env{t: t, ctx: ctx, store: store, svc: svc, queue: q, engine: eng, driver: rec, api: api, key: gen.Secret,
		project: p, endpoint: epID, clock: clk, metrics: set, kr: kr}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type response struct {
	code int
	hdr  http.Header
	body []byte
}

func (r response) obj(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return m
}

func (e *env) call(method, path string, body any, hdr map[string]string) response {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		buf, _ := json.Marshal(b)
		rd = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, e.api.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{code: resp.StatusCode, hdr: resp.Header, body: b}
}

func expect(t *testing.T, r response, status int, code string) {
	t.Helper()
	if r.code != status {
		t.Fatalf("status %d, want %d: %s", r.code, status, r.body)
	}
	if code != "" {
		if got := r.obj(t)["code"]; got != code {
			t.Fatalf("code %v, want %s: %s", got, code, r.body)
		}
	}
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func photo(tag string) []byte {
	return []byte("\xff\xd8\xff\xe0 fake jpeg payload " + tag + strings.Repeat("x", 2000))
}

func ticketBody(data []byte, ns, vis string, withSHA bool) map[string]any {
	b := map[string]any{"preset": "photo-lite", "namespace": ns, "visibility": vis, "filename": "photo.jpg",
		"content_type": "image/jpeg", "size": len(data), "metadata": map[string]any{"entity": "listing", "entity_id": "lst_77"}}
	if withSHA {
		b["sha256"] = sha(data)
	}
	return b
}

// putTicket uploads data to the presigned URL of a ticket with net/http.
func (e *env) putTicket(ticket map[string]any, data []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(ticket["method"].(string), ticket["url"].(string), bytes.NewReader(data))
	for k, v := range ticket["headers"].(map[string]any) {
		req.Header.Set(k, v.(string))
	}
	req.ContentLength = int64(len(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		e.t.Fatalf("presigned PUT: %d %s", resp.StatusCode, body)
	}
}

// upload runs ticket → PUT → complete?wait and returns the asset.
func (e *env) upload(data []byte, ns, vis string) map[string]any {
	e.t.Helper()
	r := e.call("POST", "/v1/uploads", ticketBody(data, ns, vis, false), nil)
	expect(e.t, r, 201, "")
	ticket := r.obj(e.t)
	e.putTicket(ticket, data)
	r = e.call("POST", "/v1/uploads/"+ticket["upload_id"].(string)+"/complete?wait=10s", nil, nil)
	expect(e.t, r, 200, "")
	return r.obj(e.t)
}

func (e *env) namespace(ns string) *db.Namespace {
	e.t.Helper()
	n, err := e.store.GetNamespace(e.ctx, e.project.ID, ns)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) events(typ string) []map[string]any {
	e.t.Helper()
	evs, err := e.store.ListEventsByType(e.ctx, e.project.ID, typ, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	out := []map[string]any{}
	for _, ev := range evs {
		var m map[string]any
		json.Unmarshal(ev.Payload, &m)
		m["_id"] = ev.ID
		out = append(out, m)
	}
	return out
}

func (e *env) objectExists(bucket, key string) bool {
	_, err := e.driver.Driver.Head(e.ctx, bucket, key)
	return err == nil
}

func renditionsTotal(a map[string]any) int64 {
	var n int64
	for _, r := range a["renditions"].([]any) {
		n += int64(r.(map[string]any)["bytes"].(float64))
	}
	return n
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestUploadTicketEndToEnd(t *testing.T) {
	e := newEnv(t)
	data := photo("one")

	r := e.call("POST", "/v1/uploads", ticketBody(data, "org_8f2c", "public", true), nil)
	expect(t, r, 201, "")
	ticket := r.obj(t)
	uploadID := ticket["upload_id"].(string)
	if !strings.HasPrefix(uploadID, "upl_") || ticket["method"] != "PUT" ||
		!strings.Contains(ticket["url"].(string), "/"+privateBucket+"/tmp/acme/"+uploadID) || ticket["expires_at"] == nil {
		t.Fatalf("ticket: %s", r.body)
	}
	hdrs := ticket["headers"].(map[string]any)
	if hdrs["Content-Type"] != "image/jpeg" || hdrs["Content-Length"] != strconv.Itoa(len(data)) {
		t.Fatalf("ticket headers: %v", hdrs)
	}
	if n := e.namespace("org_8f2c"); n.ReservedBytes != int64(len(data)) || n.UsedBytes != 0 {
		t.Fatalf("reservation: %+v", n)
	}
	if p, _ := e.store.GetProject(e.ctx, e.project.ID); p.ReservedBytes != int64(len(data)) {
		t.Fatalf("project reservation: %d", p.ReservedBytes)
	}

	// Complete before the PUT: the object is missing.
	expect(t, e.call("POST", "/v1/uploads/"+uploadID+"/complete", nil, nil), 409, "upload_incomplete")

	e.putTicket(ticket, data)
	r = e.call("POST", "/v1/uploads/"+uploadID+"/complete?wait=10s", nil, nil)
	expect(t, r, 200, "")
	a := r.obj(t)
	if a["status"] != "ready" || a["kind"] != "image" || a["namespace"] != "org_8f2c" || a["visibility"] != "public" ||
		a["preset"] != "photo-lite" || a["preset_version"] != float64(1) || a["ready_at"] == nil {
		t.Fatalf("asset: %s", r.body)
	}
	orig := a["original"].(map[string]any)
	if orig["filename"] != "photo.jpg" || orig["bytes"] != float64(len(data)) || orig["width"] != float64(fakeimage.Width) ||
		orig["content_type"] != "image/jpeg" {
		t.Fatalf("original: %v", orig)
	}
	if md := a["metadata"].(map[string]any); md["entity_id"] != "lst_77" {
		t.Fatalf("metadata: %v", md)
	}
	ph := a["placeholder"].(map[string]any)
	if th, err := base64.StdEncoding.DecodeString(ph["thumbhash"].(string)); err != nil || len(th) != 16 ||
		!strings.HasPrefix(ph["dominant_color"].(string), "#") {
		t.Fatalf("placeholder: %v", ph)
	}
	rends := a["renditions"].([]any)
	if len(rends) != 1 || rends[0].(map[string]any)["format"] != "avif" { // default presets: AVIF only
		t.Fatalf("renditions: %v", rends)
	}
	total := renditionsTotal(a)
	wantRatio := math.Round((1-float64(total)/float64(len(data)))*1000) / 1000
	if wantRatio <= 0 {
		t.Fatalf("test photo too small for a positive savings ratio (%d → %d bytes)", len(data), total)
	}
	if a["savings_ratio"] != wantRatio {
		t.Fatalf("savings_ratio %v, want %v", a["savings_ratio"], wantRatio)
	}
	for i, ri := range rends {
		rd := ri.(map[string]any)
		format := []string{"avif", "webp"}[i]
		u := rd["url"].(string)
		key := strings.TrimPrefix(u, cdnBase+"/")
		if rd["name"] != "main" || rd["format"] != format || !keyRe.MatchString(key) ||
			!strings.HasSuffix(key, "/main-800x600."+format) || rd["key"] != nil || rd["width"] != float64(800) || rd["height"] != float64(600) {
			t.Fatalf("rendition %d: %v", i, rd)
		}
		info, err := e.driver.Driver.Head(e.ctx, publicBucket, key)
		if err != nil || info.Size != int64(rd["bytes"].(float64)) || info.ContentType != "image/"+format {
			t.Fatalf("stored %s: %+v %v", key, info, err)
		}
		if o, _ := e.driver.opts(publicBucket, key); o.CacheControl != "public, max-age=31536000, immutable" {
			t.Fatalf("cache control of %s: %q", key, o.CacheControl)
		}
		if e.objectExists(privateBucket, key) {
			t.Fatalf("public rendition also in the private bucket: %s", key)
		}
	}
	if e.objectExists(privateBucket, "tmp/acme/"+uploadID) {
		t.Fatal("tmp object not deleted")
	}

	// Quota settled: reserved → used by the stored bytes.
	n := e.namespace("org_8f2c")
	if n.ReservedBytes != 0 || n.UsedBytes != total || n.AssetCount != 1 {
		t.Fatalf("namespace after settle: %+v (total %d)", n, total)
	}
	if p, _ := e.store.GetProject(e.ctx, e.project.ID); p.ReservedBytes != 0 || p.UsedBytes != total {
		t.Fatalf("project after settle: used %d reserved %d", p.UsedBytes, p.ReservedBytes)
	}
	u, _ := e.store.GetUsageDaily(e.ctx, e.project.ID, "org_8f2c", e.clock.Now().Format("2006-01-02"))
	if u.Uploads != 1 || u.AssetsReady != 1 || u.BytesIn != int64(len(data)) || u.BytesStored != total || u.CPUMillis != 7 {
		t.Fatalf("usage_daily: %+v", u)
	}

	// Outbox: asset.ready with a pending delivery to the endpoint.
	evs := e.events(webhook.EventAssetReady)
	if len(evs) != 1 {
		t.Fatalf("asset.ready events: %d", len(evs))
	}
	data0 := evs[0]["data"].(map[string]any)
	if data0["asset"].(map[string]any)["id"] != a["id"] || data0["metadata"].(map[string]any)["entity"] != "listing" {
		t.Fatalf("event data: %v", data0)
	}
	ds, _ := e.store.ListDeliveriesByEvent(e.ctx, evs[0]["_id"].(string))
	if len(ds) != 1 || ds[0].EndpointID != e.endpoint || ds[0].Status != db.DeliveryPending {
		t.Fatalf("deliveries: %+v", ds)
	}

	// complete is idempotent; GET and list agree.
	r2 := e.call("POST", "/v1/uploads/"+uploadID+"/complete?wait=1s", nil, nil)
	expect(t, r2, 200, "")
	if r2.obj(t)["id"] != a["id"] {
		t.Fatal("second complete returned another asset")
	}
	if g := e.call("GET", "/v1/assets/"+a["id"].(string), nil, nil); g.code != 200 || g.obj(t)["status"] != "ready" {
		t.Fatalf("get: %d %s", g.code, g.body)
	}
	l := e.call("GET", "/v1/assets?namespace=org_8f2c&status=ready", nil, nil).obj(t)
	if items := l["items"].([]any); len(items) != 1 || l["next_cursor"] != nil {
		t.Fatalf("list: %v", l)
	}

	var sb strings.Builder
	e.metrics.WritePrometheus(&sb)
	for _, m := range []string{"kadr_bytes_in_total " + strconv.Itoa(len(data)), "kadr_bytes_stored_total " + strconv.FormatInt(total, 10),
		`kadr_job_duration_seconds_bucket{kind="process_image"`, `kadr_jobs_queued{pool="image"} 0`} {
		if !strings.Contains(sb.String(), m) {
			t.Fatalf("metric %q missing:\n%s", m, sb.String())
		}
	}
}

func TestPrivateUploadStoresKeysNotURLs(t *testing.T) {
	e := newEnv(t)
	a := e.upload(photo("private"), "org_1", "private")
	if a["status"] != "ready" {
		t.Fatalf("asset: %v", a)
	}
	for _, ri := range a["renditions"].([]any) {
		rd := ri.(map[string]any)
		key, _ := rd["key"].(string)
		if rd["url"] != nil || !keyRe.MatchString(key) {
			t.Fatalf("private rendition: %v", rd)
		}
		if !e.objectExists(privateBucket, key) || e.objectExists(publicBucket, key) {
			t.Fatalf("private rendition %s in the wrong bucket", key)
		}
		if o, _ := e.driver.opts(privateBucket, key); o.CacheControl != "private, max-age=31536000, immutable" {
			t.Fatalf("cache control: %q", o.CacheControl)
		}
	}
}

func TestDedup(t *testing.T) {
	e := newEnv(t)
	data := photo("dup")
	first := e.upload(data, "org_a", "public")
	calls := e.engine.ProcessCalls.Load()

	// 1. Ticket with a known sha256 → 200 with a ready asset, no upload.
	r := e.call("POST", "/v1/uploads", ticketBody(data, "org_b", "public", true), nil)
	expect(t, r, 200, "")
	dup := r.obj(t)["asset"].(map[string]any)
	if dup["status"] != "ready" || dup["id"] == first["id"] || dup["namespace"] != "org_b" {
		t.Fatalf("dedup asset: %s", r.body)
	}
	if fmt.Sprint(dup["renditions"]) != fmt.Sprint(first["renditions"]) {
		t.Fatal("dedup asset does not share the blob renditions")
	}
	if n := e.namespace("org_b"); n.UsedBytes != renditionsTotal(first) || n.ReservedBytes != 0 || n.AssetCount != 1 {
		t.Fatalf("dedup namespace: %+v", n)
	}

	// 2. Same content without sha256 → processed, deduplicated after hashing.
	second := e.upload(data, "org_c", "public")
	if fmt.Sprint(second["renditions"]) != fmt.Sprint(first["renditions"]) {
		t.Fatal("post-hash dedup produced a new blob")
	}
	if e.engine.ProcessCalls.Load() != calls {
		t.Fatal("engine ran for duplicate content")
	}

	// 3. Dedup never crosses visibility.
	priv := e.upload(data, "org_c", "private")
	if fmt.Sprint(priv["renditions"]) == fmt.Sprint(first["renditions"]) {
		t.Fatal("private asset deduplicated against a public blob")
	}

	asset, _ := e.store.GetAsset(e.ctx, first["id"].(string))
	b, _ := e.store.GetBlob(e.ctx, *asset.BlobID)
	if b.Refcount != 3 {
		t.Fatalf("refcount = %d, want 3", b.Refcount)
	}
	var sb strings.Builder
	e.metrics.WritePrometheus(&sb)
	if !strings.Contains(sb.String(), "kadr_dedup_hits_total 2") {
		t.Fatalf("dedup metric:\n%s", sb.String())
	}
}

func TestNonRetryableFailureReleasesQuota(t *testing.T) {
	e := newEnv(t)
	data := append([]byte("CORRUPT"), photo("bad")...)
	a := e.upload(data, "org_f", "public")
	errObj, _ := a["error"].(map[string]any)
	if a["status"] != "failed" || errObj["code"] != "corrupt" || len(a["renditions"].([]any)) != 0 {
		t.Fatalf("failed asset: %v", a)
	}
	if n := e.namespace("org_f"); n.ReservedBytes != 0 || n.UsedBytes != 0 || n.AssetCount != 0 {
		t.Fatalf("quota after failure: %+v", n)
	}
	evs := e.events(webhook.EventAssetFailed)
	if len(evs) != 1 || evs[0]["data"].(map[string]any)["error"].(map[string]any)["code"] != "corrupt" {
		t.Fatalf("asset.failed events: %v", evs)
	}
	asset, _ := e.store.GetAsset(e.ctx, a["id"].(string))
	u, _ := e.store.GetUpload(e.ctx, *asset.UploadID)
	if u.Status != db.UploadFailed {
		t.Fatalf("upload status %s", u.Status)
	}
	if e.objectExists(privateBucket, *u.TmpKey) {
		t.Fatal("tmp object kept after failure")
	}
	j, _ := e.store.GetJobByDedupeKey(e.ctx, u.ID)
	if j.Status != db.JobDead || j.Attempts != 1 {
		t.Fatalf("non-retryable failure was retried: %+v", j)
	}
}

func TestRetryableFailureExhaustsAttempts(t *testing.T) {
	e := newEnv(t)
	a := e.upload(append([]byte("FLAKY"), photo("flaky")...), "org_r", "public")
	if a["status"] == "processing" {
		waitUntil(t, "asset failure", func() bool {
			got, _ := e.store.GetAsset(e.ctx, a["id"].(string))
			return got.Status == db.AssetFailed
		})
	}
	got, _ := e.store.GetAsset(e.ctx, a["id"].(string))
	if got.Status != db.AssetFailed || *got.ErrorCode != "engine_failure" {
		t.Fatalf("asset: %+v", got)
	}
	j, _ := e.store.GetJobByDedupeKey(e.ctx, *got.UploadID)
	if j.Status != db.JobDead || j.Attempts != queue.DefaultMaxAttempts {
		t.Fatalf("job: %+v", j)
	}
	if n := e.namespace("org_r"); n.ReservedBytes != 0 {
		t.Fatalf("reservation not released: %+v", n)
	}
}

func TestQuotaExceededAndWarnings(t *testing.T) {
	e := newEnv(t)
	// Namespace quota rejects a reservation that does not fit.
	r := e.call("PUT", "/v1/namespaces/org_q/quota", map[string]any{"quota_bytes": 100}, nil)
	expect(t, r, 200, "")
	if r.obj(t)["quota_bytes"] != float64(100) {
		t.Fatalf("quota: %s", r.body)
	}
	expect(t, e.call("POST", "/v1/uploads", ticketBody(photo("q"), "org_q", "public", false), nil), 402, "quota_exceeded")

	// Project quota.
	p, _ := e.store.GetProject(e.ctx, e.project.ID)
	p.QuotaBytes = ptr(int64(50))
	must(t, e.store.UpdateProject(e.ctx, p))
	expect(t, e.call("POST", "/v1/uploads", ticketBody(photo("q"), "org_other", "public", false), nil), 402, "quota_exceeded")
	p.QuotaBytes = nil
	must(t, e.store.UpdateProject(e.ctx, p))

	// quota.warning at >= 80 % and quota.exceeded at >= 100 %, once each.
	first := e.upload(photo("w1"), "org_w", "public")
	stored := renditionsTotal(first)
	expect(t, e.call("PUT", "/v1/namespaces/org_w/quota", map[string]any{"quota_bytes": stored * 11 / 10}, nil), 200, "")
	if evs := e.events(webhook.EventQuotaWarning); len(evs) != 1 || evs[0]["data"].(map[string]any)["namespace"] != "org_w" {
		t.Fatalf("quota.warning: %v", evs)
	}
	expect(t, e.call("PUT", "/v1/namespaces/org_w/quota", map[string]any{"quota_bytes": stored*11/10 + 1}, nil), 200, "")
	if evs := e.events(webhook.EventQuotaWarning); len(evs) != 1 {
		t.Fatalf("quota.warning repeated without a new crossing: %d", len(evs))
	}
	// Any new upload must fit the remaining quota: use a tiny server-side file.
	e.serverUpload(t, []byte("\xff\xd8 tiny w2"), "org_w", "public", 201)
	if evs := e.events(webhook.EventQuotaExceeded); len(evs) != 1 {
		t.Fatalf("quota.exceeded: %v", evs)
	}
	u := e.call("GET", "/v1/namespaces/org_w/usage", nil, nil).obj(t)
	if u["asset_count"] != float64(2) || u["used_bytes"].(float64) <= u["quota_bytes"].(float64) {
		t.Fatalf("usage: %v", u)
	}
}

func TestIdempotencyKey(t *testing.T) {
	e := newEnv(t)
	data := photo("idem")
	hdr := map[string]string{"Idempotency-Key": "req-123"}
	r1 := e.call("POST", "/v1/uploads", ticketBody(data, "org_i", "public", false), hdr)
	expect(t, r1, 201, "")
	r2 := e.call("POST", "/v1/uploads", ticketBody(data, "org_i", "public", false), hdr)
	expect(t, r2, 201, "")
	if !bytes.Equal(r1.body, r2.body) || r2.hdr.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay differs:\n%s\n%s", r1.body, r2.body)
	}
	if n := e.namespace("org_i"); n.ReservedBytes != int64(len(data)) {
		t.Fatalf("replay reserved quota twice: %+v", n)
	}
	other := ticketBody(data, "org_i", "private", false)
	expect(t, e.call("POST", "/v1/uploads", other, hdr), 422, "idempotency_mismatch")
	// Errors are not stored: the same key can be retried after a failure.
	bad := ticketBody(data, "org_i", "public", false)
	bad["preset"] = "missing"
	expect(t, e.call("POST", "/v1/uploads", bad, map[string]string{"Idempotency-Key": "req-err"}), 404, "preset_not_found")
	expect(t, e.call("POST", "/v1/uploads", ticketBody(data, "org_i", "public", false), map[string]string{"Idempotency-Key": "req-err"}), 201, "")
	expect(t, e.call("POST", "/v1/uploads", ticketBody(data, "org_i", "public", false), map[string]string{"Idempotency-Key": "bad key"}), 400, "invalid_idempotency_key")
}

func TestSweeperExpiredUploads(t *testing.T) {
	e := newEnv(t)
	// Ticket whose file was uploaded but complete was never called.
	data := photo("sweep")
	r := e.call("POST", "/v1/uploads", ticketBody(data, "org_s", "public", false), nil)
	expect(t, r, 201, "")
	uploaded := r.obj(t)
	e.putTicket(uploaded, data)
	// Ticket without a file.
	r = e.call("POST", "/v1/uploads", ticketBody(photo("never"), "org_s", "public", false), nil)
	expect(t, r, 201, "")
	abandoned := r.obj(t)["upload_id"].(string)

	if res, err := e.svc.SweepExpiredUploads(e.ctx); err != nil || res.Promoted+res.Expired != 0 {
		t.Fatalf("sweep before expiry: %+v %v", res, err)
	}
	e.clock.Add(pipeline.DefaultUploadTTL + pipeline.DefaultSweepGrace + time.Second)
	res, err := e.svc.SweepExpiredUploads(e.ctx)
	if err != nil || res.Promoted != 1 || res.Expired != 1 {
		t.Fatalf("sweep: %+v %v", res, err)
	}
	u, _ := e.store.GetUpload(e.ctx, uploaded["upload_id"].(string))
	waitUntil(t, "swept upload processed", func() bool {
		a, _ := e.store.GetAsset(e.ctx, *u.AssetID)
		return a.Status == db.AssetReady
	})
	ab, _ := e.store.GetUpload(e.ctx, abandoned)
	if ab.Status != db.UploadExpired {
		t.Fatalf("abandoned upload: %s", ab.Status)
	}
	evs := e.events(webhook.EventUploadExpired)
	if len(evs) != 1 || evs[0]["data"].(map[string]any)["upload_id"] != abandoned {
		t.Fatalf("upload.expired: %v", evs)
	}
	waitUntil(t, "reservations settled", func() bool { return e.namespace("org_s").ReservedBytes == 0 })
	expect(t, e.call("POST", "/v1/uploads/"+abandoned+"/complete", nil, nil), 410, "upload_expired")
}

func TestDeleteAndGC(t *testing.T) {
	e := newEnv(t)
	data := photo("gc")
	a1 := e.upload(data, "org_g", "public")
	r := e.call("POST", "/v1/uploads", ticketBody(data, "org_g", "public", true), nil)
	expect(t, r, 200, "")
	a2 := r.obj(t)["asset"].(map[string]any)
	asset, _ := e.store.GetAsset(e.ctx, a1["id"].(string))
	blobID := *asset.BlobID
	var keys []string
	for _, ri := range a1["renditions"].([]any) {
		keys = append(keys, strings.TrimPrefix(ri.(map[string]any)["url"].(string), cdnBase+"/"))
	}

	expect(t, e.call("DELETE", "/v1/assets/"+a1["id"].(string), nil, nil), 204, "")
	expect(t, e.call("GET", "/v1/assets/"+a1["id"].(string), nil, nil), 404, "not_found")
	if b, _ := e.store.GetBlob(e.ctx, blobID); b.Status != db.BlobReady || b.Refcount != 1 {
		t.Fatalf("blob after first delete: %+v", b)
	}
	expect(t, e.call("DELETE", "/v1/assets/"+a2["id"].(string), nil, nil), 204, "")
	if evs := e.events(webhook.EventAssetDeleted); len(evs) != 2 {
		t.Fatalf("asset.deleted events: %d", len(evs))
	}
	waitUntil(t, "blob GC", func() bool {
		b, _ := e.store.GetBlob(e.ctx, blobID)
		return b.Status == db.BlobDeleted
	})
	for _, k := range keys {
		if e.objectExists(publicBucket, k) {
			t.Fatalf("object %s survived GC", k)
		}
	}
	if n := e.namespace("org_g"); n.UsedBytes != 0 || n.AssetCount != 0 {
		t.Fatalf("quota after delete: %+v", n)
	}
	// The same content can be uploaded again after GC (dedup key freed).
	again := e.upload(data, "org_g", "public")
	if again["status"] != "ready" || fmt.Sprint(again["renditions"]) == fmt.Sprint(a1["renditions"]) {
		t.Fatalf("re-upload after GC: %v", again)
	}
	if n, err := e.svc.CollectGarbage(e.ctx); err != nil || n != 0 {
		t.Fatalf("periodic GC: %d %v", n, err)
	}
}

func (e *env) serverUpload(t *testing.T, data []byte, ns, vis string, want int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "receipt.jpg")
	fw.Write(data)
	mw.WriteField("preset", "doc-lite")
	mw.WriteField("namespace", ns)
	mw.WriteField("visibility", vis)
	mw.WriteField("metadata", `{"entity":"receipt"}`)
	mw.Close()
	r := e.call("POST", "/v1/assets?wait=10s", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()})
	expect(t, r, want, "")
	return r.obj(t)
}

func TestServerSideUpload(t *testing.T) {
	e := newEnv(t)
	a := e.serverUpload(t, photo("server"), "org_srv", "private", 201)
	if a["status"] != "ready" || a["preset"] != "doc-lite" || a["original"].(map[string]any)["filename"] != "receipt.jpg" ||
		a["metadata"].(map[string]any)["entity"] != "receipt" || a["placeholder"].(map[string]any)["thumbhash"] != nil {
		t.Fatalf("server asset: %v", a)
	}
	if n := e.namespace("org_srv"); n.ReservedBytes != 0 || n.AssetCount != 1 {
		t.Fatalf("quota: %+v", n)
	}
	// The staged source is removed after processing.
	if err := e.svc.CleanStaleWorkDirs(e.ctx); err != nil {
		t.Fatal(err)
	}
	// Too large.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("preset", "doc-lite")
	mw.WriteField("namespace", "org_srv")
	mw.WriteField("visibility", "private")
	fw, _ := mw.CreateFormFile("file", "big.jpg")
	fw.Write(bytes.Repeat([]byte{1}, pipeline.MaxServerUploadBytes+10))
	mw.Close()
	expect(t, e.call("POST", "/v1/assets", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()}), 413, "too_large")
}

func TestValidationAbortPatchAndPresets(t *testing.T) {
	e := newEnv(t)
	big := ticketBody(photo("x"), "org_v", "public", false)
	big["size"] = 64<<20 + 1
	r := e.call("POST", "/v1/uploads", big, nil)
	expect(t, r, 413, "too_large")
	if !strings.Contains(r.obj(t)["detail"].(string), "single upload") {
		t.Fatalf("too_large detail: %s", r.body)
	}
	overPreset := ticketBody(photo("x"), "org_v", "public", false)
	overPreset["size"] = 30 << 20
	expect(t, e.call("POST", "/v1/uploads", overPreset, nil), 413, "too_large")
	pdf := ticketBody(photo("x"), "org_v", "public", false)
	pdf["content_type"] = "application/pdf"
	expect(t, e.call("POST", "/v1/uploads", pdf, nil), 415, "unsupported_media")
	video := ticketBody(photo("x"), "org_v", "public", false)
	video["preset"] = "video-720p"
	expect(t, e.call("POST", "/v1/uploads", video, nil), 415, "unsupported_media")
	missing := ticketBody(photo("x"), "org_v", "public", false)
	missing["preset"] = "nope"
	expect(t, e.call("POST", "/v1/uploads", missing, nil), 404, "preset_not_found")
	invalid := ticketBody(photo("x"), "", "secret", false)
	r = e.call("POST", "/v1/uploads", invalid, nil)
	expect(t, r, 422, "validation_failed")
	if errs := r.obj(t)["errors"].(map[string]any); errs["namespace"] == nil || errs["visibility"] == nil {
		t.Fatalf("field errors: %s", r.body)
	}

	// Abort releases the reservation.
	r = e.call("POST", "/v1/uploads", ticketBody(photo("abort"), "org_v", "public", false), nil)
	expect(t, r, 201, "")
	id := r.obj(t)["upload_id"].(string)
	expect(t, e.call("DELETE", "/v1/uploads/"+id, nil, nil), 204, "")
	expect(t, e.call("DELETE", "/v1/uploads/"+id, nil, nil), 204, "")
	if n := e.namespace("org_v"); n.ReservedBytes != 0 {
		t.Fatalf("abort kept reservation: %+v", n)
	}
	expect(t, e.call("POST", "/v1/uploads/"+id+"/complete", nil, nil), 409, "upload_aborted")
	expect(t, e.call("POST", "/v1/uploads/upl_missing/complete", nil, nil), 404, "not_found")

	// PATCH metadata.
	a := e.upload(photo("patch"), "org_v", "public")
	id = a["id"].(string)
	r = e.call("PATCH", "/v1/assets/"+id, map[string]any{"metadata": map[string]any{"entity": "listing", "entity_id": "lst_99"}}, nil)
	expect(t, r, 200, "")
	if r.obj(t)["metadata"].(map[string]any)["entity_id"] != "lst_99" || len(e.events(webhook.EventAssetUpdated)) != 1 {
		t.Fatalf("patch: %s", r.body)
	}
	expect(t, e.call("PATCH", "/v1/assets/"+id, map[string]any{"metadata": []int{1}}, nil), 422, "validation_failed")

	// Presets.
	r = e.call("GET", "/v1/presets", nil, nil)
	expect(t, r, 200, "")
	if items := r.obj(t)["items"].([]any); len(items) != len(preset.DefaultPresets()) {
		t.Fatalf("presets: %s", r.body)
	}
	r = e.call("GET", "/v1/presets/avatar", nil, nil)
	expect(t, r, 200, "")
	if p := r.obj(t); p["slug"] != "avatar" || p["version"] != float64(1) || p["params"].(map[string]any)["kind"] != "image" {
		t.Fatalf("preset: %s", r.body)
	}
	expect(t, e.call("GET", "/v1/presets/nope", nil, nil), 404, "preset_not_found")

	// Pagination.
	e.upload(photo("page2"), "org_v", "public")
	l := e.call("GET", "/v1/assets?namespace=org_v&limit=1", nil, nil).obj(t)
	cur, _ := l["next_cursor"].(string)
	if len(l["items"].([]any)) != 1 || cur == "" {
		t.Fatalf("page 1: %v", l)
	}
	l2 := e.call("GET", "/v1/assets?namespace=org_v&limit=1&cursor="+cur, nil, nil).obj(t)
	if len(l2["items"].([]any)) != 1 || l2["items"].([]any)[0].(map[string]any)["id"] == l["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("page 2: %v", l2)
	}
	expect(t, e.call("GET", "/v1/assets?cursor=!!!", nil, nil), 422, "validation_failed")
	expect(t, e.call("GET", "/v1/signed-urls", nil, nil), 501, "not_implemented")
}

func ptr[T any](v T) *T { return &v }

// setDefaultMode changes the default compression mode of the test project.
func (e *env) setDefaultMode(mode string) {
	e.t.Helper()
	p, err := e.store.GetProject(e.ctx, e.project.ID)
	must(e.t, err)
	p.DefaultMode = mode
	must(e.t, e.store.UpdateProject(e.ctx, p))
}

// keyRe matches the storage key layout of keys.go.
var keyRe = regexp.MustCompile(`^acme/image/\d{4}/\d{2}/blb_[0-9a-z]+/[a-z]+-\d+x\d+\.(avif|webp)$`)
