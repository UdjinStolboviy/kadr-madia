// Package webhook implements the transactional outbox (events +
// webhook_deliveries, ADR-0011) and delivery following the Standard Webhooks
// specification (https://www.standardwebhooks.com/).
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
)

// SecretPrefix prefixes every endpoint secret.
const SecretPrefix = "whsec_"

// secretBytes is the size of a generated signing key (spec: 24–64 bytes).
const secretBytes = 32

// DefaultTolerance is the accepted clock skew of webhook-timestamp.
const DefaultTolerance = 5 * time.Minute

// Errors returned by Verify.
var (
	ErrInvalidSecret    = errors.New("webhook: invalid secret")
	ErrMissingHeaders   = errors.New("webhook: missing webhook-id, webhook-timestamp or webhook-signature")
	ErrTimestamp        = errors.New("webhook: timestamp outside the tolerance window")
	ErrNoValidSignature = errors.New("webhook: no matching signature")
)

// GenerateSecret returns a new random secret "whsec_<base64>".
func GenerateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webhook: generate secret: %w", err)
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(b), nil
}

// secretKey decodes the HMAC key of a "whsec_<base64>" secret.
func secretKey(secret string) ([]byte, error) {
	s := strings.TrimPrefix(secret, SecretPrefix)
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

// Sign returns the webhook-signature header value
// "v1,<base64(HMAC-SHA256(key, "{id}.{timestamp}.{body}"))>".
func Sign(secret, msgID string, timestamp int64, body []byte) (string, error) {
	key, err := secretKey(secret)
	if err != nil {
		return "", err
	}
	return "v1," + base64.StdEncoding.EncodeToString(mac(key, msgID, timestamp, body)), nil
}

func mac(key []byte, msgID string, timestamp int64, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msgID))
	h.Write([]byte{'.'})
	h.Write([]byte(strconv.FormatInt(timestamp, 10)))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// Verify checks a received webhook the way a receiver should: the timestamp
// must be within tolerance of now and at least one space-separated "v1,"
// signature must match (constant-time comparison).
func Verify(secret, msgID, timestampHeader, signatureHeader string, body []byte, now time.Time, tolerance time.Duration) error {
	if msgID == "" || timestampHeader == "" || signatureHeader == "" {
		return ErrMissingHeaders
	}
	key, err := secretKey(secret)
	if err != nil {
		return err
	}
	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrTimestamp
	}
	want := mac(key, msgID, ts, body)
	for _, part := range strings.Fields(signatureHeader) {
		version, sig, ok := strings.Cut(part, ",")
		if !ok || version != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrNoValidSignature
}

// SecretAAD binds an encrypted secret to its endpoint record.
func SecretAAD(endpointID string) []byte { return []byte("webhook_endpoint:" + endpointID) }

// EncryptSecret seals a secret for webhook_endpoints.secret_enc.
func EncryptSecret(kr *crypto.Keyring, endpointID, secret string) ([]byte, error) {
	return kr.Encrypt([]byte(secret), SecretAAD(endpointID))
}

// DecryptSecret opens webhook_endpoints.secret_enc.
func DecryptSecret(kr *crypto.Keyring, endpointID string, enc []byte) (string, error) {
	pt, err := kr.Decrypt(enc, SecretAAD(endpointID))
	if err != nil {
		return "", fmt.Errorf("decrypt webhook secret of %s: %w", endpointID, err)
	}
	return string(pt), nil
}
