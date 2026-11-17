package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/pquerna/otp/totp"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/public"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

const (
	testEmail    = "admin@example.com"
	testPassword = "correct horse battery staple"
)

var testPepper = []byte("test-pepper-test-pepper-test-pepper")

type env struct {
	t        *testing.T
	store    *db.Store
	admin    http.Handler
	public   http.Handler
	webhooks *webhook.Dispatcher
}

func newEnv(t *testing.T, ui fstest.MapFS) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "kadr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := db.DefaultMigrator().Up(ctx, d.W); err != nil {
		t.Fatal(err)
	}
	store := db.NewStore(d)
	key := make([]byte, crypto.KeySize)
	rand.Read(key)
	kr, _ := crypto.NewKeyring(key)

	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdmin(ctx, &db.Admin{ID: db.NewID(db.PrefixAdmin), Email: testEmail, PasswordHash: hash,
		Role: "owner", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	set := metrics.NewSet()
	cfg := Config{Store: store, Keyring: kr, Logger: log, Metrics: set, Dev: true, Pepper: testPepper,
		Build: BuildInfo{Version: "test", Commit: "abc", StartedAt: time.Now()}, TmpDir: t.TempDir(),
		FallbackHTML: []byte("<p>fallback</p>"),
		Webhooks:     webhook.New(webhook.Config{Store: store, Keyring: kr, Logger: log})}
	disp := cfg.Webhooks
	if ui != nil {
		cfg.UI = ui
	}
	pub := public.Handler(public.Config{Store: store, Logger: log, Metrics: set,
		Verifier: &auth.KeyVerifier{Store: store, Pepper: testPepper}})
	return &env{t: t, store: store, admin: New(cfg).Handler(), public: pub, webhooks: disp}
}

type client struct {
	e      *env
	cookie *http.Cookie
	csrf   string
}

type resp struct {
	code int
	hdr  http.Header
	body []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return m
}

func (c *client) do(method, path string, body any, withCSRF bool) resp {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if withCSRF && c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	rec := httptest.NewRecorder()
	c.e.admin.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cookieDev {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	return resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.Bytes()}
}

func (c *client) login(t *testing.T, extra map[string]any) resp {
	t.Helper()
	body := map[string]any{"email": testEmail, "password": testPassword}
	for k, v := range extra {
		body[k] = v
	}
	r := c.do("POST", "/admin/api/auth/login", body, false)
	if r.code == http.StatusOK {
		c.csrf = r.json(t)["csrf_token"].(string)
	}
	return r
}

func expectProblem(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.code != status {
		t.Fatalf("status = %d, want %d; body %s", r.code, status, r.body)
	}
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content-type = %q", ct)
	}
	var p map[string]any
	json.Unmarshal(r.body, &p)
	if p["code"] != code {
		t.Fatalf("code = %v, want %s; body %s", p["code"], code, r.body)
	}
}

func TestAuthFlowAndCSRF(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}

	expectProblem(t, c.do("GET", "/admin/api/projects", nil, false), 401, "unauthenticated")
	expectProblem(t, c.do("GET", "/admin/api/auth/me", nil, false), 401, "unauthenticated")

	bad := c.do("POST", "/admin/api/auth/login", map[string]any{"email": testEmail, "password": "nope"}, false)
	expectProblem(t, bad, 401, "invalid_credentials")
	expectProblem(t, c.do("POST", "/admin/api/auth/login", map[string]any{"email": "x@example.com", "password": "nope"}, false), 401, "invalid_credentials")
	expectProblem(t, c.do("POST", "/admin/api/auth/login", map[string]any{"email": ""}, false), 422, "validation_failed")

	r := c.login(t, nil)
	if r.code != 200 {
		t.Fatalf("login: %d %s", r.code, r.body)
	}
	m := r.json(t)
	if m["admin"].(map[string]any)["email"] != testEmail || c.csrf == "" || c.cookie == nil {
		t.Fatalf("login body %s", r.body)
	}
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", c.cookie)
	}

	me := c.do("GET", "/admin/api/auth/me", nil, false)
	if me.code != 200 || me.json(t)["csrf_token"] != c.csrf {
		t.Fatalf("me: %d %s", me.code, me.body)
	}

	// CSRF: missing and wrong token.
	expectProblem(t, c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme"}, false), 403, "csrf_failed")
	saved := c.csrf
	c.csrf = "wrong"
	expectProblem(t, c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme"}, true), 403, "csrf_failed")
	c.csrf = saved

	if r := c.do("POST", "/admin/api/auth/logout", nil, true); r.code != 204 {
		t.Fatalf("logout: %d %s", r.code, r.body)
	}
	expectProblem(t, c.do("GET", "/admin/api/auth/me", nil, false), 401, "unauthenticated")
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}
	for i := range 5 {
		r := c.do("POST", "/admin/api/auth/login", map[string]any{"email": testEmail, "password": "wrong"}, false)
		if r.code != 401 {
			t.Fatalf("attempt %d: %d", i, r.code)
		}
	}
	r := c.do("POST", "/admin/api/auth/login", map[string]any{"email": testEmail, "password": testPassword}, false)
	expectProblem(t, r, 429, "rate_limited")
	if r.hdr.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestProjectsKeysPresetsAndPublicPing(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}
	c.login(t, nil)

	expectProblem(t, c.do("POST", "/admin/api/projects", map[string]any{"slug": "1bad", "name": ""}, true), 422, "validation_failed")
	r := c.do("POST", "/admin/api/projects", map[string]any{"slug": "x", "name": "X", "environment": "prod"}, true)
	expectProblem(t, r, 422, "validation_failed")
	errs := r.json(t)["errors"].(map[string]any)
	if errs["slug"] == nil || errs["environment"] == nil {
		t.Fatalf("errors = %v", errs)
	}

	r = c.do("POST", "/admin/api/projects", map[string]any{
		"slug": "acme-staging", "name": "Acme Staging", "environment": "test",
		"allowed_origins": []string{"https://app.example.com/", "http://localhost:3000"},
	}, true)
	if r.code != 201 {
		t.Fatalf("create project: %d %s", r.code, r.body)
	}
	p := r.json(t)
	pid := p["id"].(string)
	if !strings.HasPrefix(pid, "prj_") || p["quota_bytes"] != nil || p["disabled"] != false || p["storage_profile_id"] != nil {
		t.Fatalf("project = %s", r.body)
	}
	if o := p["allowed_origins"].([]any); len(o) != 2 || o[0] != "https://app.example.com" {
		t.Fatalf("origins = %v", o)
	}
	expectProblem(t, c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme-staging", "name": "Dup"}, true), 409, "slug_taken")

	r = c.do("GET", "/admin/api/projects", nil, false)
	if r.code != 200 || len(r.json(t)["items"].([]any)) != 1 {
		t.Fatalf("list: %s", r.body)
	}
	expectProblem(t, c.do("GET", "/admin/api/projects/prj_missing", nil, false), 404, "not_found")

	r = c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"name": "Renamed", "quota_bytes": 1000, "disabled": true}, true)
	if r.code != 200 || r.json(t)["quota_bytes"].(float64) != 1000 || r.json(t)["disabled"] != true {
		t.Fatalf("patch: %s", r.body)
	}
	r = c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"quota_bytes": nil, "disabled": false}, true)
	if r.code != 200 || r.json(t)["quota_bytes"] != nil || r.json(t)["name"] != "Renamed" {
		t.Fatalf("patch null: %s", r.body)
	}

	// Default presets.
	r = c.do("GET", "/admin/api/projects/"+pid+"/presets", nil, false)
	items := r.json(t)["items"].([]any)
	if len(items) != 12 {
		t.Fatalf("presets = %d", len(items))
	}
	slugs := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		slugs[m["slug"].(string)] = m
	}
	for _, s := range []string{"photo-lite", "photo-balanced", "doc-lite", "avatar", "video-720p", "video-480p-eco"} {
		if slugs[s] == nil || slugs[s]["current_version"].(float64) != 1 {
			t.Fatalf("missing preset %s", s)
		}
	}
	params := slugs["photo-lite"]["params"].(map[string]any)
	if params["quality"].(map[string]any)["maxBytes"].(float64) != 20480 || params["kind"] != "image" {
		t.Fatalf("photo-lite params = %v", params)
	}

	// API keys.
	expectProblem(t, c.do("POST", "/admin/api/projects/"+pid+"/api-keys", map[string]any{"name": "k", "scopes": []string{"root"}}, true), 422, "validation_failed")
	r = c.do("POST", "/admin/api/projects/"+pid+"/api-keys", map[string]any{"name": "backend", "scopes": []string{"uploads:write", "assets:read"}}, true)
	if r.code != 201 {
		t.Fatalf("create key: %d %s", r.code, r.body)
	}
	kb := r.json(t)
	secret := kb["secret"].(string)
	key := kb["api_key"].(map[string]any)
	if !strings.HasPrefix(secret, "kd_test_") || !strings.HasPrefix(secret, key["prefix"].(string)+"_") {
		t.Fatalf("secret %q prefix %v", secret, key["prefix"])
	}
	r = c.do("GET", "/admin/api/projects/"+pid+"/api-keys", nil, false)
	if lst := r.json(t)["items"].([]any); len(lst) != 1 || strings.Contains(string(r.body), secret) {
		t.Fatalf("list keys: %s", r.body)
	}

	ping := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/ping", nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		e.public.ServeHTTP(rec, req)
		return rec
	}
	rec := ping(secret)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"project":"acme-staging"`) {
		t.Fatalf("ping: %d %s", rec.Code, rec.Body)
	}
	if rec := ping(""); rec.Code != 401 {
		t.Fatalf("ping no key: %d", rec.Code)
	}
	// Flip the last character to a value guaranteed to differ from the original.
	last := secret[len(secret)-1]
	bad := byte('x')
	if last == 'x' {
		bad = 'y'
	}
	if rec := ping(secret[:len(secret)-1] + string(bad)); rec.Code != 401 {
		t.Fatalf("ping bad key: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec = httptest.NewRecorder()
	e.public.ServeHTTP(rec, req)
	if rec.Code != 501 {
		t.Fatalf("unimplemented route: %d", rec.Code)
	}

	// Disabling the project blocks the key.
	c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"disabled": true}, true)
	if rec := ping(secret); rec.Code != 403 {
		t.Fatalf("ping disabled project: %d", rec.Code)
	}
	c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"disabled": false}, true)

	if r := c.do("DELETE", "/admin/api/api-keys/"+key["id"].(string), nil, true); r.code != 204 {
		t.Fatalf("revoke: %d %s", r.code, r.body)
	}
	if rec := ping(secret); rec.Code != 401 {
		t.Fatalf("ping revoked: %d", rec.Code)
	}
	expectProblem(t, c.do("DELETE", "/admin/api/api-keys/key_missing", nil, true), 404, "not_found")

	// Audit trail.
	r = c.do("GET", "/admin/api/audit?limit=2", nil, false)
	ab := r.json(t)
	if len(ab["items"].([]any)) != 2 || ab["next_cursor"] == nil {
		t.Fatalf("audit: %s", r.body)
	}
	r = c.do("GET", "/admin/api/audit?limit=200&cursor="+ab["next_cursor"].(string), nil, false)
	if r.code != 200 {
		t.Fatalf("audit page 2: %s", r.body)
	}
	actions := map[string]bool{}
	for _, it := range c.do("GET", "/admin/api/audit?limit=200", nil, false).json(t)["items"].([]any) {
		actions[it.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"auth.login", "project.create", "project.update", "key.create", "key.revoke"} {
		if !actions[a] {
			t.Fatalf("missing audit action %s in %v", a, actions)
		}
	}
}

func TestStorageProfiles(t *testing.T) {
	backend := s3mem.New()
	backend.CreateBucket("kadr-public")
	backend.CreateBucket("kadr-private")
	fake := httptest.NewServer(gofakes3.New(backend).Server())
	defer fake.Close()

	e := newEnv(t, nil)
	c := &client{e: e}
	c.login(t, nil)

	input := map[string]any{
		"name": "Platform R2", "provider": "r2", "endpoint": fake.URL, "region": "auto",
		"public_bucket": "kadr-public", "private_bucket": "kadr-private",
		"public_base_url": fake.URL + "/kadr-public", "access_key_id": "AKIAEXAMPLE1234", "secret_access_key": "s3cr3t",
		"is_platform": true,
	}
	bad := map[string]any{"name": "x", "provider": "ftp", "endpoint": "nope", "public_bucket": "a", "private_bucket": "a"}
	r := c.do("POST", "/admin/api/storage-profiles", bad, true)
	expectProblem(t, r, 422, "validation_failed")

	r = c.do("POST", "/admin/api/storage-profiles", input, true)
	if r.code != 201 {
		t.Fatalf("create: %d %s", r.code, r.body)
	}
	sp := r.json(t)
	id := sp["id"].(string)
	if sp["access_key_id"] != "AKIA…1234" || strings.Contains(string(r.body), "s3cr3t") || sp["secret_access_key"] != nil {
		t.Fatalf("profile leaks or bad mask: %s", r.body)
	}

	r = c.do("POST", "/admin/api/storage-profiles/"+id+"/test", nil, true)
	if r.code != 200 {
		t.Fatalf("test: %d %s", r.code, r.body)
	}
	tr := r.json(t)
	if tr["ok"] != true || len(tr["checks"].([]any)) != 7 {
		t.Fatalf("test result: %s", r.body)
	}
	r = c.do("GET", "/admin/api/storage-profiles/"+id, nil, false)
	if g := r.json(t); g["last_test_ok"] != true || g["last_test_at"] == nil {
		t.Fatalf("get after test: %s", r.body)
	}

	// PATCH: rename, echo masked key back (must be ignored), credentials kept.
	r = c.do("PATCH", "/admin/api/storage-profiles/"+id, map[string]any{"name": "Renamed", "access_key_id": "AKIA…1234"}, true)
	if r.code != 200 || r.json(t)["name"] != "Renamed" || r.json(t)["access_key_id"] != "AKIA…1234" {
		t.Fatalf("patch: %s", r.body)
	}
	if r := c.do("POST", "/admin/api/storage-profiles/"+id+"/test", nil, true); r.json(t)["ok"] != true {
		t.Fatalf("test after patch: %s", r.body)
	}
	// Wrong public base URL → that check fails.
	c.do("PATCH", "/admin/api/storage-profiles/"+id, map[string]any{"public_base_url": fake.URL + "/missing"}, true)
	r = c.do("POST", "/admin/api/storage-profiles/"+id+"/test", nil, true)
	if r.json(t)["ok"] != false {
		t.Fatalf("expected failing test: %s", r.body)
	}

	// In use by a project → 409; after unassigning → 204.
	r = c.do("POST", "/admin/api/projects", map[string]any{"slug": "demo", "name": "Demo", "storage_profile_id": id}, true)
	if r.code != 201 {
		t.Fatalf("project with profile: %s", r.body)
	}
	pid := r.json(t)["id"].(string)
	expectProblem(t, c.do("DELETE", "/admin/api/storage-profiles/"+id, nil, true), 409, "in_use")
	c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"storage_profile_id": nil}, true)
	if r := c.do("DELETE", "/admin/api/storage-profiles/"+id, nil, true); r.code != 204 {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	expectProblem(t, c.do("GET", "/admin/api/storage-profiles/"+id, nil, false), 404, "not_found")
	r = c.do("POST", "/admin/api/projects", map[string]any{"slug": "demo2", "name": "Demo", "storage_profile_id": "stp_nope"}, true)
	expectProblem(t, r, 422, "validation_failed")
}

func TestTOTPFlow(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}
	c.login(t, nil)

	r := c.do("POST", "/admin/api/auth/totp/setup", nil, true)
	if r.code != 200 {
		t.Fatalf("setup: %d %s", r.code, r.body)
	}
	secret := r.json(t)["secret"].(string)
	if !strings.HasPrefix(r.json(t)["otpauth_url"].(string), "otpauth://totp/") {
		t.Fatalf("setup body %s", r.body)
	}
	expectProblem(t, c.do("POST", "/admin/api/auth/totp/enable", map[string]any{"code": "000000"}, true), 400, "invalid_code")
	code, _ := totp.GenerateCode(secret, time.Now())
	if r := c.do("POST", "/admin/api/auth/totp/enable", map[string]any{"code": code}, true); r.code != 204 {
		t.Fatalf("enable: %d %s", r.code, r.body)
	}
	if me := c.do("GET", "/admin/api/auth/me", nil, false); me.json(t)["admin"].(map[string]any)["totp_enabled"] != true {
		t.Fatalf("me: %s", me.body)
	}
	c.do("POST", "/admin/api/auth/logout", nil, true)

	c2 := &client{e: e}
	expectProblem(t, c2.login(t, nil), 401, "totp_required")
	expectProblem(t, c2.login(t, map[string]any{"totp_code": "123456"}), 401, "invalid_credentials")
	code, _ = totp.GenerateCode(secret, time.Now())
	if r := c2.login(t, map[string]any{"totp_code": code}); r.code != 200 {
		t.Fatalf("login with totp: %d %s", r.code, r.body)
	}
	expectProblem(t, c2.do("POST", "/admin/api/auth/totp/disable", map[string]any{"password": "wrong"}, true), 401, "invalid_credentials")
	if r := c2.do("POST", "/admin/api/auth/totp/disable", map[string]any{"password": testPassword}, true); r.code != 204 {
		t.Fatalf("disable: %d %s", r.code, r.body)
	}
}

func TestSystemAndSPA(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}
	c.login(t, nil)
	r := c.do("GET", "/admin/api/system", nil, false)
	if r.code != 200 {
		t.Fatalf("system: %d %s", r.code, r.body)
	}
	sys := r.json(t)
	if sys["version"] != "test" || sys["dev"] != true || sys["capabilities"].(map[string]any)["vips"] == nil {
		t.Fatalf("system: %s", r.body)
	}
	expectProblem(t, c.do("GET", "/admin/api/nope", nil, false), 404, "not_found")

	// Fallback page when the UI is not built.
	r = c.do("GET", "/projects/123", nil, false)
	if r.code != 200 || !strings.Contains(string(r.body), "fallback") {
		t.Fatalf("fallback: %d %s", r.code, r.body)
	}
	if r := c.do("GET", "/healthz", nil, false); r.code != 200 {
		t.Fatalf("healthz: %d", r.code)
	}
	if r := c.do("GET", "/readyz", nil, false); r.code != 200 {
		t.Fatalf("readyz: %d %s", r.code, r.body)
	}

	// Built UI: files served, unknown routes → index.html, missing assets → 404.
	e2 := newEnv(t, fstest.MapFS{
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	})
	c2 := &client{e: e2}
	r = c2.do("GET", "/", nil, false)
	if r.code != 200 || string(r.body) != "<html>app</html>" || r.hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("index: %d %q %v", r.code, r.body, r.hdr)
	}
	r = c2.do("GET", "/assets/app-1.js", nil, false)
	if r.code != 200 || !strings.Contains(r.hdr.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", r.code, r.hdr)
	}
	if r := c2.do("GET", "/settings/storage", nil, false); string(r.body) != "<html>app</html>" {
		t.Fatalf("deep link: %q", r.body)
	}
	if r := c2.do("GET", "/assets/missing.js", nil, false); r.code != 404 {
		t.Fatalf("missing asset: %d", r.code)
	}
	expectProblem(t, c2.do("GET", "/admin/api/auth/me", nil, false), 401, "unauthenticated")
}

func TestViewerIsReadOnly(t *testing.T) {
	e := newEnv(t, nil)
	hash, _ := auth.HashPassword(testPassword)
	e.store.CreateAdmin(context.Background(), &db.Admin{ID: db.NewID(db.PrefixAdmin), Email: "viewer@example.com",
		PasswordHash: hash, Role: "viewer", CreatedAt: time.Now()})
	c := &client{e: e}
	r := c.do("POST", "/admin/api/auth/login", map[string]any{"email": "viewer@example.com", "password": testPassword}, false)
	c.csrf = r.json(t)["csrf_token"].(string)
	if r := c.do("GET", "/admin/api/projects", nil, false); r.code != 200 {
		t.Fatalf("viewer read: %d", r.code)
	}
	expectProblem(t, c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme"}, true), 403, "forbidden")
	if r := c.do("POST", "/admin/api/auth/logout", nil, true); r.code != 204 {
		t.Fatalf("viewer logout: %d", r.code)
	}
}

func TestCSPAllowsInlineScriptsByHash(t *testing.T) {
	inline := "\n      document.documentElement.dataset.theme = 'dark';\n    "
	csp := buildCSP(fstest.MapFS{"index.html": {Data: []byte(
		"<html><head><script>" + inline + "</script><script type=\"module\" src=\"/assets/a.js\"></script></head></html>")}})
	sum := sha256.Sum256([]byte(inline))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(csp, "script-src 'self' "+want+";") {
		t.Fatalf("csp = %s", csp)
	}
	if got := buildCSP(nil); !strings.Contains(got, "script-src 'self';") {
		t.Fatalf("csp without UI = %s", got)
	}
}
