package cfaccess

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(t *testing.T, k *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifierAndMiddleware(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	fetches := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		fetches++
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer srv.Close()
	team := strings.TrimPrefix(srv.URL, "https://")
	now := time.Now()
	v := &Verifier{TeamDomain: team, Audiences: []string{"aud-1"}, Client: srv.Client(), Now: func() time.Time { return now }}
	good := map[string]any{"iss": "https://" + team, "aud": []string{"aud-1"}, "exp": now.Add(time.Hour).Unix(), "email": "a@example.com"}

	c, err := v.Verify(context.Background(), sign(t, key, "k1", good))
	if err != nil || c.Email != "a@example.com" {
		t.Fatalf("valid token: %v %v", c, err)
	}
	cases := map[string]map[string]any{
		"wrong aud": {"iss": "https://" + team, "aud": "other", "exp": now.Add(time.Hour).Unix()},
		"wrong iss": {"iss": "https://evil.example", "aud": "aud-1", "exp": now.Add(time.Hour).Unix()},
		"expired":   {"iss": "https://" + team, "aud": "aud-1", "exp": now.Add(-time.Hour).Unix()},
	}
	for name, claims := range cases {
		if _, err := v.Verify(context.Background(), sign(t, key, "k1", claims)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := v.Verify(context.Background(), sign(t, other, "k1", good)); err == nil {
		t.Error("foreign signature accepted")
	}
	if _, err := v.Verify(context.Background(), "a.b.c"); err == nil {
		t.Error("garbage accepted")
	}
	if fetches != 1 {
		t.Errorf("certs fetched %d times, want 1 (cached)", fetches)
	}

	h := v.Middleware([]string{"/healthz", "/mcp", "/.well-known/"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for path, want := range map[string]int{"/healthz": 204, "/mcp": 204, "/.well-known/oauth-protected-resource": 204, "/admin/api/projects": 403} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s without token: %d, want %d", path, rec.Code, want)
		}
	}
	req := httptest.NewRequest("GET", "/admin/api/projects", nil)
	req.Header.Set(Header, sign(t, key, "k1", good))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Errorf("with token: %d", rec.Code)
	}
}
