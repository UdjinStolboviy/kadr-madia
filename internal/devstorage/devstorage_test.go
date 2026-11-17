package devstorage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
	"github.com/UdjinStolboviy/kadr-madia/internal/devstorage"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

func start(t *testing.T, dir string) *devstorage.Server {
	t.Helper()
	s, err := devstorage.Start(devstorage.Config{Addr: "127.0.0.1:0", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s
}

func driver(t *testing.T, s *devstorage.Server) storage.Driver {
	t.Helper()
	d, err := storage.NewS3(storage.Config{Provider: "s3", Endpoint: s.Endpoint, Region: "us-east-1",
		AccessKeyID: devstorage.AccessKeyID, SecretAccessKey: devstorage.SecretAccessKey})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRejectsNonLoopback(t *testing.T) {
	if _, err := devstorage.Start(devstorage.Config{Addr: "0.0.0.0:0"}); err == nil {
		t.Fatal("expected an error for a non-loopback address")
	}
}

func TestPresignedPutPublicGetAndPersistence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := start(t, dir)
	d := driver(t, s)

	// Presigned PUT from a "browser": CORS preflight, then the PUT itself.
	body := []byte("hello dev storage")
	pr, err := d.PresignPut(ctx, devstorage.PrivateBucket, "tmp/acme/upl_1", storage.PresignPutOptions{
		ContentType: "image/jpeg", ContentLength: int64(len(body)), TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := http.NewRequest(http.MethodOptions, pr.URL, nil)
	pre.Header.Set("Origin", "http://localhost:5173")
	pre.Header.Set("Access-Control-Request-Method", "PUT")
	pre.Header.Set("Access-Control-Request-Headers", "content-type")
	res, err := http.DefaultClient.Do(pre)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Access-Control-Allow-Origin") == "" || !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("preflight not allowed: %v", res.Header)
	}
	put, _ := http.NewRequest(http.MethodPut, pr.URL, bytes.NewReader(body))
	put.Header.Set("Content-Type", "image/jpeg")
	put.Header.Set("Content-Length", strconv.Itoa(len(body)))
	put.ContentLength = int64(len(body))
	res, err = http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("presigned PUT: %d", res.StatusCode)
	}
	info, err := d.Head(ctx, devstorage.PrivateBucket, "tmp/acme/upl_1")
	if err != nil || info.Size != int64(len(body)) {
		t.Fatalf("head: %+v %v", info, err)
	}

	// Public objects are readable at <endpoint>/kadr-public/<key>.
	if err := d.Put(ctx, devstorage.PublicBucket, "acme/blb_1/main.webp", bytes.NewReader(body), int64(len(body)),
		storage.PutOptions{ContentType: "image/webp"}); err != nil {
		t.Fatal(err)
	}
	get := func(url string) (int, string, string) {
		res, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type"), string(b)
	}
	if code, ct, got := get(s.PublicBaseURL() + "/acme/blb_1/main.webp"); code != 200 || ct != "image/webp" || got != string(body) {
		t.Fatalf("public GET: %d %q %q", code, ct, got)
	}

	// Objects survive a restart when Dir is set.
	_ = s.Shutdown(ctx)
	s2 := start(t, dir)
	if code, _, got := get(s2.PublicBaseURL() + "/acme/blb_1/main.webp"); code != 200 || got != string(body) {
		t.Fatalf("after restart: %d %q", code, got)
	}
}

func TestEnsureProfile(t *testing.T) {
	ctx := context.Background()
	store := dbtest.Open(t)
	key := make([]byte, crypto.KeySize)
	_, _ = rand.Read(key)
	kr, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p, created, err := devstorage.EnsureProfile(ctx, store, kr, "http://127.0.0.1:9000", now)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if !p.IsPlatform || p.PublicBaseURL != "http://127.0.0.1:9000/kadr-public" || p.PrivateBucket != devstorage.PrivateBucket {
		t.Fatalf("unexpected profile: %+v", p)
	}
	pt, err := kr.Decrypt(p.CredentialsEnc, []byte("storage_profile:"+p.ID))
	if err != nil {
		t.Fatal(err)
	}
	var creds map[string]string
	if err := json.Unmarshal(pt, &creds); err != nil || creds["access_key_id"] != devstorage.AccessKeyID || creds["secret_access_key"] == "" {
		t.Fatalf("credentials: %v %v", creds, err)
	}

	// Second start: no new profile; a changed address updates it in place.
	if _, created, err := devstorage.EnsureProfile(ctx, store, kr, "http://127.0.0.1:9000", now); err != nil || created {
		t.Fatalf("second call: %v %v", created, err)
	}
	p2, created, err := devstorage.EnsureProfile(ctx, store, kr, "http://127.0.0.1:9100", now.Add(time.Second))
	if err != nil || created || p2.ID != p.ID || p2.Endpoint != "http://127.0.0.1:9100" || p2.PublicBaseURL != "http://127.0.0.1:9100/kadr-public" {
		t.Fatalf("update: %+v %v %v", p2, created, err)
	}
	all, _ := store.ListStorageProfiles(ctx)
	if len(all) != 1 {
		t.Fatalf("profiles: %d", len(all))
	}
}
