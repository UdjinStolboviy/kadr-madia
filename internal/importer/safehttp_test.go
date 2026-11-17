package importer

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIsBlocked(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0",
		"::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "224.0.0.1", "240.0.0.1"} {
		if !IsBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if IsBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s must be allowed", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for _, bad := range []string{"ftp://x.com/a", "file:///etc/passwd", "http://user:pw@x.com/", "http://x.com:22/", "//x.com", "", "http:///a"} {
		if _, err := ValidateURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ValidateURL("https://cdn.example.com/a.jpg"); err != nil {
		t.Fatal(err)
	}
}

type fakeResolver map[string][]netip.Addr

func (r fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestBlocksLoopbackAndRebindingMix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	var buf bytes.Buffer
	f := &Fetcher{AllowAnyPort: true}
	_, _, _, err := f.Get(context.Background(), srv.URL, 1<<20, &buf) // 127.0.0.1
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("loopback: %v", err)
	}
	// A name resolving to a public AND a private address is rejected as a whole.
	f = &Fetcher{AllowAnyPort: true, Resolver: fakeResolver{"evil.test": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}}}
	_, _, _, err = f.Get(context.Background(), "http://evil.test/x.jpg", 1<<20, &buf)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("mixed: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatal("data was read")
	}
}

func TestGetRedirectsLimitsAndStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(bytes.Repeat([]byte("a"), 100))
	})
	mux.HandleFunc("/r1", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/evil", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "ftp://x/y", http.StatusFound) })
	mux.HandleFunc("/gone", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := &Fetcher{AllowAnyPort: true, AllowAddr: func(netip.Addr) bool { return true }}
	get := func(path string, max int64) (int64, string, error) {
		var b bytes.Buffer
		n, ct, _, err := f.Get(context.Background(), srv.URL+path, max, &b)
		return n, ct, err
	}
	if n, ct, err := get("/r1", 1000); err != nil || n != 100 || ct != "image/jpeg" {
		t.Fatalf("redirect: %d %q %v", n, ct, err)
	}
	if _, _, err := get("/ok", 50); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size: %v", err)
	}
	if _, _, err := get("/loop", 50); err == nil {
		t.Fatal("redirect loop")
	}
	if _, _, err := get("/evil", 50); err == nil {
		t.Fatal("redirect to ftp accepted")
	}
	var se *StatusError
	if _, _, err := get("/gone", 50); !errors.As(err, &se) || !se.Permanent() {
		t.Fatalf("404: %v", err)
	}
}
