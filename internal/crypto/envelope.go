// Package crypto implements envelope encryption for secrets stored in the
// database (storage credentials, TOTP secrets, webhook secrets, OAuth tokens).
//
// A key-encryption key (KEK, the master key) lives outside the database. Every
// record gets its own random 256-bit data-encryption key (DEK). The DEK is
// wrapped with the KEK using AES-256-GCM and stored next to the ciphertext, so
// rotating the master key only requires re-wrapping DEKs.
//
// Blob format v1 (all fields fixed-size except the ciphertext):
//
//	version (1 byte = 0x01)
//	kek_id  (4 bytes, first bytes of SHA-256(KEK) — detects a wrong master key)
//	wrap_nonce (12) | wrapped_dek (32 + 16 tag)
//	data_nonce (12) | ciphertext (+16 tag)
//
// The header (version || kek_id) is authenticated as additional data of both
// GCM operations; the caller-provided AAD (for example "storage_profile:<id>")
// is authenticated with the data so a blob cannot be moved to another record.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// KeySize is the size of the master key and of every DEK.
	KeySize = 32

	versionV1   = 0x01
	kekIDSize   = 4
	nonceSize   = 12
	tagSize     = 16
	headerSize  = 1 + kekIDSize
	wrappedSize = KeySize + tagSize
	// Overhead is the number of bytes a v1 blob adds to the plaintext.
	Overhead = headerSize + nonceSize + wrappedSize + nonceSize + tagSize
)

var (
	// ErrUnsupportedVersion is returned for blobs with an unknown format version.
	ErrUnsupportedVersion = errors.New("crypto: unsupported blob version")
	// ErrWrongKey is returned when a blob was encrypted with another master key.
	ErrWrongKey = errors.New("crypto: blob was encrypted with a different master key")
	// ErrDecrypt is returned when authentication fails (tampering or wrong AAD).
	ErrDecrypt = errors.New("crypto: decryption failed")
	// ErrMalformed is returned for truncated blobs.
	ErrMalformed = errors.New("crypto: malformed blob")
)

// Keyring holds the master key (KEK). It is safe for concurrent use.
type Keyring struct {
	kek  cipher.AEAD
	id   [kekIDSize]byte
	rand io.Reader
}

// NewKeyring builds a keyring from a raw 32-byte master key.
func NewKeyring(key []byte) (*Keyring, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: master key must be %d bytes, got %d", KeySize, len(key))
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	k := &Keyring{kek: aead, rand: rand.Reader}
	sum := sha256.Sum256(key)
	copy(k.id[:], sum[:kekIDSize])
	return k, nil
}

// ParseKey decodes master key material: 32 raw bytes, base64 (standard or
// URL, padded or not) or 64 hex characters. Surrounding whitespace is ignored
// for the textual forms.
func ParseKey(data []byte) ([]byte, error) {
	if len(data) == KeySize {
		return append([]byte(nil), data...), nil
	}
	s := strings.TrimSpace(string(data))
	if len(s) == 2*KeySize {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("crypto: master key must be %d raw bytes, base64 or hex", KeySize)
}

// LoadKeyFile reads and parses a master key file.
func LoadKeyFile(path string) (*Keyring, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("crypto: read master key: %w", err)
	}
	key, err := ParseKey(data)
	if err != nil {
		return nil, fmt.Errorf("%w (file %s)", err, path)
	}
	return NewKeyring(key)
}

// EnsureKeyFile creates a new random base64-encoded master key at path with
// mode 0600 if the file does not exist. It reports whether a key was created.
// Intended for dev mode only; production keys are provisioned by an operator.
func EnsureKeyFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("crypto: stat master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("crypto: create key dir: %w", err)
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return false, fmt.Errorf("crypto: generate key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("crypto: create master key: %w", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return false, fmt.Errorf("crypto: write master key: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("crypto: write master key: %w", err)
	}
	return true, nil
}

// Encrypt seals plaintext with a fresh DEK. aad binds the blob to its context
// (record type and id) and must be passed unchanged to Decrypt.
func (k *Keyring) Encrypt(plaintext, aad []byte) ([]byte, error) {
	dek := make([]byte, KeySize)
	if _, err := io.ReadFull(k.rand, dek); err != nil {
		return nil, fmt.Errorf("crypto: generate dek: %w", err)
	}
	defer clear(dek)

	out := make([]byte, headerSize, Overhead+len(plaintext))
	out[0] = versionV1
	copy(out[1:headerSize], k.id[:])
	header := out[:headerSize:headerSize]

	wrapNonce, err := k.nonce()
	if err != nil {
		return nil, err
	}
	out = append(out, wrapNonce...)
	out = k.kek.Seal(out, wrapNonce, dek, header)

	dataAEAD, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	dataNonce, err := k.nonce()
	if err != nil {
		return nil, err
	}
	out = append(out, dataNonce...)
	out = dataAEAD.Seal(out, dataNonce, plaintext, dataAAD(header, aad))
	return out, nil
}

// Decrypt opens a blob produced by Encrypt.
func (k *Keyring) Decrypt(blob, aad []byte) ([]byte, error) {
	if len(blob) < Overhead {
		return nil, ErrMalformed
	}
	if blob[0] != versionV1 {
		return nil, ErrUnsupportedVersion
	}
	header := blob[:headerSize:headerSize]
	if subtle.ConstantTimeCompare(header[1:], k.id[:]) != 1 {
		return nil, ErrWrongKey
	}
	p := headerSize
	wrapNonce := blob[p : p+nonceSize]
	p += nonceSize
	wrapped := blob[p : p+wrappedSize]
	p += wrappedSize
	dek, err := k.kek.Open(nil, wrapNonce, wrapped, header)
	if err != nil {
		return nil, ErrDecrypt
	}
	defer clear(dek)

	dataAEAD, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	dataNonce := blob[p : p+nonceSize]
	p += nonceSize
	pt, err := dataAEAD.Open(nil, dataNonce, blob[p:], dataAAD(header, aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

func (k *Keyring) nonce() ([]byte, error) {
	n := make([]byte, nonceSize)
	if _, err := io.ReadFull(k.rand, n); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	return n, nil
}

func dataAAD(header, aad []byte) []byte {
	out := make([]byte, 0, len(header)+len(aad))
	return append(append(out, header...), aad...)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	return aead, nil
}
