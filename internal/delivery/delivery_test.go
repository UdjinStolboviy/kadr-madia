package delivery_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/UdjinStolboviy/kadr-madia/internal/delivery"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

const secret = "s3cret"

type resolver struct {
	g     *delivery.Grant
	calls atomic.Int32
	fail  error
}

func (r *resolver) DeliveryGrant(_ context.Context, kid string) (*delivery.Grant, error) {
	r.calls.Add(1)
	if r.fail != nil {
		return nil, r.fail
	}
	if kid != "k1" {
		return nil, delivery.ErrUnknownKey
	}
	return r.g, nil
}

type fixture struct {
	srv  *httptest.Server
	res  *resolver
	now  time.Time
	data []byte
}

func setup(t *testing.T, cfg delivery.Config) *fixture {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket("priv"); err != nil {
		t.Fatal(err)
	}
	s3srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(s3srv.Close)
	d, err := storage.NewS3(storage.Config{Provider: "r2", Endpoint: s3srv.URL, Region: "auto", AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := bytes.Repeat([]byte("0123456789"), 1000)
	put := func(key, ct, disp string, body []byte) {
		if err := d.Put(ctx, "priv", key, bytes.NewReader(body), int64(len(body)), storage.PutOptions{ContentType: ct, ContentDisposition: disp}); err != nil {
			t.Fatal(err)
		}
	}
	put("acme/video/2026/11/blb_1/main-720p-av1.mp4", "video/mp4", "", data)
	put("acme/file/2026/11/blb_2/main.pdf", "application/pdf", `attachment; filename="a.pdf"`, []byte("%PDF"))
	put("acme/video/2026/11/blb_3/hls/master.m3u8", "application/vnd.apple.mpegurl", "", []byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n720p.m3u8\nhttps://x/abs.m3u8\n"))
	put("other/image/2026/11/blb_9/main.avif", "image/avif", "", []byte("x"))

	f := &fixture{res: &resolver{g: &delivery.Grant{Secret: []byte(secret), Prefix: "acme/", Driver: d, Bucket: "priv"}},
		now: time.Unix(1_800_000_000, 0), data: data}
	cfg.Resolver = f.res
	cfg.Now = func() time.Time { return f.now }
	f.srv = httptest.NewServer(delivery.New(cfg))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) url(key string, exp int64, download string) string {
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "kid": {"k1"}, "sig": {pipeline.SignKey(secret, key, exp, download)}}
	if download != "" {
		q.Set("download", download)
	}
	return f.srv.URL + "/" + key + "?" + q.Encode()
}

func do(t *testing.T, method, u string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func TestServeObjectRangeAndConditional(t *testing.T) {
	f := setup(t, delivery.Config{})
	exp := f.now.Unix() + 600
	u := f.url("acme/video/2026/11/blb_1/main-720p-av1.mp4", exp, "")

	resp, body := do(t, "GET", u, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, f.data) || resp.Header.Get("Content-Type") != "video/mp4" ||
		resp.Header.Get("Cache-Control") != "private, max-age=600" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("Cloudflare-CDN-Cache-Control") != "" {
		t.Fatalf("full: %d %v", resp.StatusCode, resp.Header)
	}
	etag := resp.Header.Get("ETag")

	resp, body = do(t, "GET", u, map[string]string{"Range": "bytes=10-19"})
	if resp.StatusCode != 206 || string(body) != "0123456789" || resp.Header.Get("Content-Range") != "bytes 10-19/10000" {
		t.Fatalf("range: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = do(t, "GET", u, map[string]string{"Range": "bytes=-5"})
	if resp.StatusCode != 206 || string(body) != "56789" {
		t.Fatalf("suffix range: %d %q", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", u, map[string]string{"Range": "bytes=20000-"})
	if resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */10000" {
		t.Fatalf("unsatisfiable: %d %v", resp.StatusCode, resp.Header)
	}
	resp, body = do(t, "GET", u, map[string]string{"Range": "bytes=0-9", "If-Range": `"stale"`})
	if resp.StatusCode != 200 || len(body) != len(f.data) {
		t.Fatalf("if-range mismatch must give the full body: %d", resp.StatusCode)
	}
	resp, body = do(t, "GET", u, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != 304 || len(body) != 0 {
		t.Fatalf("conditional: %d", resp.StatusCode)
	}
	resp, body = do(t, "HEAD", u, nil)
	if resp.StatusCode != 200 || len(body) != 0 || resp.Header.Get("Content-Length") != "10000" {
		t.Fatalf("head: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestDisposition(t *testing.T) {
	f := setup(t, delivery.Config{})
	exp := f.now.Unix() + 60
	resp, _ := do(t, "GET", f.url("acme/file/2026/11/blb_2/main.pdf", exp, ""), nil)
	if resp.Header.Get("Content-Disposition") != `attachment; filename="a.pdf"` {
		t.Fatalf("stored disposition: %v", resp.Header)
	}
	resp, _ = do(t, "GET", f.url("acme/file/2026/11/blb_2/main.pdf", exp, "звіт.pdf"), nil)
	if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename="____.pdf"; filename*=UTF-8''%D0%B7%D0%B2%D1%96%D1%82.pdf` {
		t.Fatalf("download disposition: %q", got)
	}
}

func TestRefusals(t *testing.T) {
	f := setup(t, delivery.Config{})
	key := "acme/video/2026/11/blb_1/main-720p-av1.mp4"
	exp := f.now.Unix() + 60
	good := f.url(key, exp, "")
	cases := map[string]string{
		"no params":         f.srv.URL + "/" + key,
		"bad signature":     strings.Replace(good, "sig=", "sig=AAAA", 1),
		"other key":         strings.Replace(good, "main-720p", "main-1080p", 1),
		"expired":           f.url(key, f.now.Unix()-1, ""),
		"unknown kid":       strings.Replace(good, "kid=k1", "kid=k2", 1),
		"download added":    good + "&download=x.mp4",
		"dot segment":       f.url("acme/../other/image/2026/11/blb_9/main.avif", exp, ""),
		"foreign project":   f.url("other/image/2026/11/blb_9/main.avif", exp, ""),
		"retained original": f.url("originals/acme/video/2026/11/blb_1.mov", exp, ""),
	}
	for name, u := range cases {
		if resp, _ := do(t, "GET", u, nil); resp.StatusCode != 403 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "GET", f.url("acme/missing.avif", exp, ""), nil); resp.StatusCode != 404 {
		t.Errorf("missing object: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", good, nil); resp.StatusCode != 405 {
		t.Errorf("post: %d", resp.StatusCode)
	}
	// Unknown kids are cached: repeated requests do not reach the resolver.
	before := f.res.calls.Load()
	for range 3 {
		do(t, "GET", strings.Replace(good, "kid=k1", "kid=k2", 1), nil)
	}
	if f.res.calls.Load() != before {
		t.Errorf("unknown kid resolved %d more times", f.res.calls.Load()-before)
	}
}

func TestResolverFailureIs503(t *testing.T) {
	f := setup(t, delivery.Config{})
	f.res.fail = errors.New("db down")
	if resp, _ := do(t, "GET", f.url("acme/file/2026/11/blb_2/main.pdf", f.now.Unix()+60, ""), nil); resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPlaylistPrefixSignature(t *testing.T) {
	f := setup(t, delivery.Config{})
	key := "acme/video/2026/11/blb_3/hls/master.m3u8"
	exp := f.now.Unix() + 300
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "kid": {"k1"}, "sig": {pipeline.SignKeyPrefix(secret, key, exp)}, "prefix": {"1"}}
	resp, body := do(t, "GET", f.srv.URL+"/"+key+"?"+q.Encode(), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("playlist: %d", resp.StatusCode)
	}
	s := string(body)
	if !strings.Contains(s, `URI="init.mp4?exp=`) || !strings.Contains(s, "\n720p.m3u8?exp=") || !strings.Contains(s, "\nhttps://x/abs.m3u8\n") ||
		resp.Header.Get("Cache-Control") != "private, max-age=60" {
		t.Fatalf("rewrite: %q %v", s, resp.Header)
	}
	// The directory signature does not reach outside the directory.
	resp, _ = do(t, "GET", f.srv.URL+"/acme/video/2026/11/blb_1/main-720p-av1.mp4?"+q.Encode(), nil)
	if resp.StatusCode != 403 {
		t.Fatalf("outside dir: %d", resp.StatusCode)
	}
}

func TestCDNCacheHeader(t *testing.T) {
	f := setup(t, delivery.Config{CDNMaxAge: 30 * time.Minute})
	resp, _ := do(t, "GET", f.url("acme/file/2026/11/blb_2/main.pdf", f.now.Unix()+600, ""), nil)
	if resp.Header.Get("Cloudflare-CDN-Cache-Control") != "max-age=600" {
		t.Fatalf("cdn header: %v", resp.Header)
	}
}

func TestBusy(t *testing.T) {
	block := make(chan struct{})
	h := delivery.New(delivery.Config{MaxStreams: 1, Resolver: blockingResolver(block)})
	srv := httptest.NewServer(h)
	defer srv.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Get(srv.URL + "/acme/a?exp=9999999999&kid=k1&sig=" + strings.Repeat("A", 43))
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(srv.URL + "/acme/a?exp=9999999999&kid=k1&sig=" + strings.Repeat("A", 43))
	if err != nil || resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("busy: %v %v", resp, err)
	}
	resp.Body.Close()
	close(block)
	<-done
}

type blockingResolver chan struct{}

func (b blockingResolver) DeliveryGrant(context.Context, string) (*delivery.Grant, error) {
	<-b
	return nil, delivery.ErrUnknownKey
}

func TestCanonicalMatchesPipeline(t *testing.T) {
	if delivery.CanonicalString("a/b.avif", 5, "x") != "GET\n/a/b.avif\n5\nx" || delivery.CanonicalPrefixString("a/", 5) != "GETP\n/a/\n5\n" {
		t.Fatal("canonical strings drifted from pipeline.SignKey / kadr-edge")
	}
	if k, ok := delivery.KeyFromPath("/a/%D1%84%20b.avif"); !ok || k != "a/ф b.avif" {
		t.Fatalf("key %q", k)
	}
	for _, p := range []string{"/", "/a/%2e%2e/b", "/a/%00"} {
		if _, ok := delivery.KeyFromPath(p); ok {
			t.Errorf("%s accepted", p)
		}
	}
}
