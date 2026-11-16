package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestKeyring(t *testing.T) *Keyring {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	k, err := NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	k := newTestKeyring(t)
	for _, pt := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte("secret"), 1000)} {
		blob, err := k.Encrypt(pt, []byte("storage_profile:stp_1"))
		if err != nil {
			t.Fatal(err)
		}
		if len(blob) != Overhead+len(pt) {
			t.Fatalf("blob len = %d, want %d", len(blob), Overhead+len(pt))
		}
		got, err := k.Decrypt(blob, []byte("storage_profile:stp_1"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("roundtrip mismatch")
		}
	}
}

func TestEncryptIsRandomized(t *testing.T) {
	k := newTestKeyring(t)
	a, _ := k.Encrypt([]byte("same"), nil)
	b, _ := k.Encrypt([]byte("same"), nil)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions produced identical blobs")
	}
}

func TestTamperDetection(t *testing.T) {
	k := newTestKeyring(t)
	blob, err := k.Encrypt([]byte("top secret"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	// Flip every byte after the header one at a time: each must fail.
	for i := headerSize; i < len(blob); i++ {
		bad := append([]byte(nil), blob...)
		bad[i] ^= 0x01
		if _, err := k.Decrypt(bad, []byte("aad")); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("byte %d: err = %v, want ErrDecrypt", i, err)
		}
	}
	if _, err := k.Decrypt(blob, []byte("other")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong aad: err = %v", err)
	}
	if _, err := k.Decrypt(blob[:Overhead-1], []byte("aad")); !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncated: err = %v", err)
	}
	bad := append([]byte(nil), blob...)
	bad[0] = 0x02
	if _, err := k.Decrypt(bad, []byte("aad")); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("version: err = %v", err)
	}
}

func TestWrongKey(t *testing.T) {
	a, b := newTestKeyring(t), newTestKeyring(t)
	blob, _ := a.Encrypt([]byte("x"), nil)
	if _, err := b.Decrypt(blob, nil); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("err = %v, want ErrWrongKey", err)
	}
}

func TestParseKey(t *testing.T) {
	raw := make([]byte, KeySize)
	rand.Read(raw)
	cases := map[string][]byte{
		"raw":    raw,
		"base64": []byte(base64.StdEncoding.EncodeToString(raw) + "\n"),
		"rawurl": []byte(base64.RawURLEncoding.EncodeToString(raw)),
		"hex":    []byte(hex.EncodeToString(raw) + "\n"),
	}
	for name, in := range cases {
		got, err := ParseKey(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("%s: mismatch", name)
		}
	}
	if _, err := ParseKey([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestEnsureKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "master.key")
	created, err := EnsureKeyFile(path)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
	k1, err := LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err = EnsureKeyFile(path)
	if err != nil || created {
		t.Fatalf("second call created=%v err=%v", created, err)
	}
	k2, _ := LoadKeyFile(path)
	blob, _ := k1.Encrypt([]byte("x"), nil)
	if _, err := k2.Decrypt(blob, nil); err != nil {
		t.Fatalf("key changed between loads: %v", err)
	}
}
