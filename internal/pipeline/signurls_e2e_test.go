package pipeline_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/delivery"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

func TestSignedURLsEndpoint(t *testing.T) {
	e := newEnv(t)
	a := e.upload(photo("priv"), "org_s", "private")
	id := a["id"].(string)
	body := map[string]any{"items": []map[string]any{{"asset_id": id, "rendition": "main.avif", "ttl": 600, "download": "r.avif"}}}

	expect(t, e.call("POST", "/v1/signed-urls", body, nil), 409, "no_signing_key")

	enc, err := e.kr.Encrypt([]byte("s3cret"), pipeline.SigningKeySecretAAD("acme-k1"))
	must(t, err)
	must(t, e.store.RotateSigningKey(e.ctx, &db.SigningKey{ID: "acme-k1", ProjectID: e.project.ID, SecretEnc: enc, CreatedAt: e.clock.Now()}))

	r := e.call("POST", "/v1/signed-urls", body, nil)
	expect(t, r, 200, "")
	item := r.obj(t)["items"].([]any)[0].(map[string]any)
	u, err := url.Parse(item["url"].(string))
	if err != nil || u.Host != "media.example.com" || !strings.HasSuffix(u.Path, "/main-800x600.avif") {
		t.Fatalf("url %v %v", item["url"], err)
	}
	q := u.Query()
	key := strings.TrimPrefix(u.Path, "/")
	var exp int64
	for _, c := range q.Get("exp") {
		exp = exp*10 + int64(c-'0')
	}
	if q.Get("kid") != "acme-k1" || q.Get("download") != "r.avif" || q.Get("sig") != pipeline.SignKey("s3cret", key, exp, "r.avif") {
		t.Fatalf("params %v", q)
	}

	// Public assets are not signable.
	pub := e.upload(photo("pub"), "org_s", "public")
	body["items"] = []map[string]any{{"asset_id": pub["id"], "rendition": "main"}}
	expect(t, e.call("POST", "/v1/signed-urls", body, nil), 409, "not_signable")
}

// The private delivery host comes from the project's storage profile when set
// (one kadr-edge per Cloudflare account), else from KADR_EDGE_BASE_URL.
func TestSignedURLsUseProfilePrivateBaseURL(t *testing.T) {
	e := newEnv(t)
	a := e.upload(photo("priv-host"), "org_h", "private")
	enc, err := e.kr.Encrypt([]byte("s3cret"), pipeline.SigningKeySecretAAD("acme-k1"))
	must(t, err)
	must(t, e.store.RotateSigningKey(e.ctx, &db.SigningKey{ID: "acme-k1", ProjectID: e.project.ID, SecretEnc: enc, CreatedAt: e.clock.Now()}))

	sign := func() string {
		r := e.call("POST", "/v1/signed-urls", map[string]any{"items": []map[string]any{{"asset_id": a["id"], "rendition": "main.avif"}}}, nil)
		expect(t, r, 200, "")
		return r.obj(t)["items"].([]any)[0].(map[string]any)["url"].(string)
	}
	if u := sign(); !strings.HasPrefix(u, "https://media.example.com/") {
		t.Fatalf("default host: %s", u)
	}
	prof, err := e.store.GetStorageProfile(e.ctx, *e.project.StorageProfileID)
	must(t, err)
	host := "https://media.client.example"
	prof.PrivateBaseURL = &host
	prof.UpdatedAt = prof.UpdatedAt.Add(time.Second)
	must(t, e.store.UpdateStorageProfile(e.ctx, prof))
	if u := sign(); !strings.HasPrefix(u, "https://media.client.example/") {
		t.Fatalf("profile host: %s", u)
	}
}

// Origin delivery: a URL from POST /v1/signed-urls is served by the delivery
// handler from the project's private bucket.
func TestSignedURLsServedByOriginDelivery(t *testing.T) {
	e := newEnv(t)
	a := e.upload(photo("priv-origin"), "org_o", "private")
	enc, err := e.kr.Encrypt([]byte("s3cret"), pipeline.SigningKeySecretAAD("acme-k1"))
	must(t, err)
	must(t, e.store.RotateSigningKey(e.ctx, &db.SigningKey{ID: "acme-k1", ProjectID: e.project.ID, SecretEnc: enc, CreatedAt: e.clock.Now()}))
	srv := httptest.NewServer(delivery.New(delivery.Config{Resolver: e.svc, Now: e.clock.Now}))
	defer srv.Close()

	r := e.call("POST", "/v1/signed-urls", map[string]any{"items": []map[string]any{{"asset_id": a["id"], "rendition": "main.avif", "download": "фото.avif"}}}, nil)
	expect(t, r, 200, "")
	signed, _ := url.Parse(r.obj(t)["items"].([]any)[0].(map[string]any)["url"].(string))
	local, _ := url.Parse(srv.URL)
	signed.Scheme, signed.Host = local.Scheme, local.Host

	resp, err := http.Get(signed.String())
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(body) == 0 || resp.Header.Get("Content-Type") != "image/avif" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "filename*=UTF-8''%D1%84%D0%BE%D1%82%D0%BE.avif") {
		t.Fatalf("delivery: %d %v %d bytes", resp.StatusCode, resp.Header, len(body))
	}

	// A tampered path is refused.
	bad := *signed
	bad.Path = strings.Replace(bad.Path, "main-", "main2-", 1)
	resp, err = http.Get(bad.String())
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("tampered: %d", resp.StatusCode)
	}
}
