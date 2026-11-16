package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"time"
)

// Session lifetime policy: a session expires after SessionIdle of inactivity
// (sliding window, extended on use) and never lives longer than
// SessionAbsolute from login.
const (
	SessionIdle     = 12 * time.Hour
	SessionAbsolute = 7 * 24 * time.Hour
	// SessionExtendEvery throttles expiry updates to one write per interval.
	SessionExtendEvery = time.Minute
)

// SessionExpiry returns the expiry for a session created at created and
// last used at now.
func SessionExpiry(created, now time.Time) time.Time {
	exp := now.Add(SessionIdle)
	if abs := created.Add(SessionAbsolute); exp.After(abs) {
		exp = abs
	}
	return exp
}

// NewSessionToken returns a random 32-byte token (base64url) and its SHA-256.
func NewSessionToken() (token string, hash []byte, err error) {
	token, err = randomToken(32)
	if err != nil {
		return "", nil, err
	}
	return token, HashToken(token), nil
}

// NewCSRFToken returns a random 32-byte token (base64url).
func NewCSRFToken() (string, error) { return randomToken(32) }

// HashToken returns SHA-256 of a session token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// EqualConstantTime compares two strings in constant time.
func EqualConstantTime(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
