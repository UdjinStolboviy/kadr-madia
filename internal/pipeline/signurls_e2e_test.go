package pipeline_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
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
	if err != nil || u.Host != "media.example.com" || !strings.HasSuffix(u.Path, "/main.avif") {
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
