// Package auth implements admin authentication (argon2id passwords, TOTP,
// server-side sessions, CSRF tokens, login rate limiting) and project API
// keys (generation, verification middleware, scope checks).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the argon2id cost parameters.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultArgon2 balances security against the 2 GB LXC budget (64 MiB per hash).
var DefaultArgon2 = Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 2, SaltLen: 16, KeyLen: 32}

// ErrInvalidHash is returned for malformed PHC strings.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// MinPasswordLen is the minimal admin password length.
const MinPasswordLen = 12

// HashPassword hashes pw with argon2id and returns a PHC string:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash> (raw standard base64).
func HashPassword(pw string) (string, error) {
	return hashWith(pw, DefaultArgon2)
}

func hashWith(pw string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks pw against a PHC argon2id string in constant time.
func VerifyPassword(encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, ErrInvalidHash
	}
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 || p.Memory > 1<<20 {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrInvalidHash
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when the account does not exist, so that
// login timing does not reveal which emails are registered.
var dummyHash = func() string {
	h, err := HashPassword("kadr-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()

// DummyVerify burns the same CPU/memory as a real verification.
func DummyVerify(pw string) {
	_, _ = VerifyPassword(dummyHash, pw)
}
