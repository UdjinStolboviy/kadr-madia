package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression: in production mode the console must work over plain HTTP on a
// LAN address (browsers drop Secure / __Host- cookies there) and use the
// hardened __Host- cookie when the request arrived over HTTPS.
func TestSessionCookieOverHTTPAndHTTPS(t *testing.T) {
	e := newEnvDev(t, nil, false) // production mode

	login := func(hdr map[string]string) *http.Cookie {
		b, _ := json.Marshal(map[string]any{"email": testEmail, "password": testPassword})
		req := httptest.NewRequest("POST", "/admin/api/auth/login", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.admin.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("login: %d %s", rec.Code, rec.Body)
		}
		for _, c := range rec.Result().Cookies() {
			return c
		}
		t.Fatal("no cookie")
		return nil
	}
	me := func(c *http.Cookie) int {
		req := httptest.NewRequest("GET", "/admin/api/auth/me", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		e.admin.ServeHTTP(rec, req)
		return rec.Code
	}

	plain := login(nil)
	if plain.Secure || plain.Name == "__Host-kadr_session" {
		t.Fatalf("plain HTTP must not get a Secure/__Host- cookie: %+v", plain)
	}
	if me(plain) != 200 {
		t.Fatal("session over plain HTTP does not work")
	}
	secure := login(map[string]string{"X-Forwarded-Proto": "https"})
	if !secure.Secure || secure.Name != "__Host-kadr_session" {
		t.Fatalf("HTTPS must get the __Host- cookie: %+v", secure)
	}
	if me(secure) != 200 {
		t.Fatal("session over HTTPS does not work")
	}
}
