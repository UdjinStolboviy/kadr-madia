package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestPasswordHashVerify(t *testing.T) {
	// Cheap parameters keep the test fast; format and logic are identical.
	h, err := hashWith("correct horse battery", Argon2Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected PHC string %q", h)
	}
	ok, err := VerifyPassword(h, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
	ok, err = VerifyPassword(h, "wrong")
	if err != nil || ok {
		t.Fatalf("wrong password accepted: %v %v", ok, err)
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=x$AA$AA", "$argon2id$v=19$m=1024,t=1,p=1$!!$AA"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestDefaultPasswordHash(t *testing.T) {
	h, err := HashPassword("another password")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword(h, "another password"); !ok {
		t.Fatal("default params verify failed")
	}
}

func TestAPIKeyGenerateParse(t *testing.T) {
	pepper := []byte("pepper-pepper-pepper-pepper-1234")
	k, err := GenerateAPIKey("live", pepper)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Secret, k.Prefix+"_") || len(k.Prefix) != len("kd_live_")+8 || len(k.Secret) != len(k.Prefix)+1+32 {
		t.Fatalf("bad key %q prefix %q", k.Secret, k.Prefix)
	}
	prefix, env, err := ParseAPIKey(k.Secret)
	if err != nil || prefix != k.Prefix || env != "live" {
		t.Fatalf("parse: %q %q %v", prefix, env, err)
	}
	if !bytes.Equal(HashAPIKey(k.Secret, pepper), k.Hash) {
		t.Fatal("hash mismatch")
	}
	if bytes.Equal(HashAPIKey(k.Secret, []byte("other")), k.Hash) {
		t.Fatal("pepper not applied")
	}
	tk, _ := GenerateAPIKey("test", pepper)
	if _, env, _ := ParseAPIKey(tk.Secret); env != "test" {
		t.Fatalf("env = %q", env)
	}
	for _, bad := range []string{"", "kd_prod_abcdefgh_" + strings.Repeat("a", 32), "kd_live_abc_" + strings.Repeat("a", 32),
		"kd_live_abcdefgh_" + strings.Repeat("a", 31), "kd_live_abcd-fgh_" + strings.Repeat("a", 32), "xx_live_abcdefgh_" + strings.Repeat("a", 32)} {
		if _, _, err := ParseAPIKey(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := GenerateAPIKey("prod", pepper); err == nil {
		t.Fatal("accepted env prod")
	}
}

func TestScopes(t *testing.T) {
	if !ValidScope("uploads:write") || ValidScope("admin") {
		t.Fatal("ValidScope wrong")
	}
	p := &Principal{Scopes: []string{ScopeAssetsRead}}
	if !p.HasScope(ScopeAssetsRead) || p.HasScope(ScopeAssetsDelete) {
		t.Fatal("HasScope wrong")
	}
}

func TestTOTP(t *testing.T) {
	secret, url, err := GenerateTOTP("admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "otpauth://totp/Kadr:admin@example.com") {
		t.Fatalf("url = %q", url)
	}
	now := time.Now()
	code, _ := totp.GenerateCode(secret, now)
	if !ValidateTOTP(code, secret, now) {
		t.Fatal("valid code rejected")
	}
	if !ValidateTOTP(code, secret, now.Add(30*time.Second)) {
		t.Fatal("skew not tolerated")
	}
	if ValidateTOTP(code, secret, now.Add(5*time.Minute)) {
		t.Fatal("stale code accepted")
	}
	if ValidateTOTP("12345", secret, now) {
		t.Fatal("short code accepted")
	}
}

func TestSessionExpiry(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := SessionExpiry(created, created); !got.Equal(created.Add(SessionIdle)) {
		t.Fatalf("got %v", got)
	}
	late := created.Add(SessionAbsolute - time.Hour)
	if got := SessionExpiry(created, late); !got.Equal(created.Add(SessionAbsolute)) {
		t.Fatalf("absolute cap not applied: %v", got)
	}
	tok, hash, err := NewSessionToken()
	if err != nil || !bytes.Equal(HashToken(tok), hash) || len(tok) < 40 {
		t.Fatal("session token")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewRateLimiter(5, time.Minute)
	l.now = func() time.Time { return now }
	for i := range 5 {
		if !l.Allow("k") {
			t.Fatalf("attempt %d denied", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("6th attempt allowed")
	}
	if !l.Allow("other") {
		t.Fatal("independent key denied")
	}
	now = now.Add(12 * time.Second) // one token refilled
	if !l.Allow("k") || l.Allow("k") {
		t.Fatal("refill wrong")
	}
}
