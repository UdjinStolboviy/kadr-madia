package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// API key scopes (docs/ADMIN_API.md).
const (
	ScopeUploadsWrite    = "uploads:write"
	ScopeAssetsRead      = "assets:read"
	ScopeAssetsWrite     = "assets:write"
	ScopeAssetsDelete    = "assets:delete"
	ScopeURLsSign        = "urls:sign"
	ScopeNamespacesAdmin = "namespaces:admin"
	ScopeImportsWrite    = "imports:write"
)

// AllScopes lists every valid scope.
var AllScopes = []string{
	ScopeUploadsWrite, ScopeAssetsRead, ScopeAssetsWrite, ScopeAssetsDelete,
	ScopeURLsSign, ScopeNamespacesAdmin, ScopeImportsWrite,
}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool {
	for _, v := range AllScopes {
		if v == s {
			return true
		}
	}
	return false
}

const (
	base62           = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	keyPrefixChars   = 8
	keySecretChars   = 32
	apiKeyMarker     = "kd"
	envLive, envTest = "live", "test"
)

// ErrMalformedKey is returned when a string is not a Kadr API key.
var ErrMalformedKey = errors.New("auth: malformed api key")

// GeneratedKey is a freshly created API key. Secret is shown to the user once.
type GeneratedKey struct {
	Secret string // kd_live_XXXXXXXX_<32 base62>
	Prefix string // kd_live_XXXXXXXX
	Hash   []byte // SHA-256(secret || pepper)
}

// GenerateAPIKey creates a key for environment "live" or "test".
func GenerateAPIKey(env string, pepper []byte) (GeneratedKey, error) {
	if env != envLive && env != envTest {
		return GeneratedKey{}, fmt.Errorf("auth: invalid key environment %q", env)
	}
	p, err := randomBase62(keyPrefixChars)
	if err != nil {
		return GeneratedKey{}, err
	}
	s, err := randomBase62(keySecretChars)
	if err != nil {
		return GeneratedKey{}, err
	}
	prefix := apiKeyMarker + "_" + env + "_" + p
	secret := prefix + "_" + s
	return GeneratedKey{Secret: secret, Prefix: prefix, Hash: HashAPIKey(secret, pepper)}, nil
}

// ParseAPIKey validates the shape of a key and returns its prefix and env.
func ParseAPIKey(secret string) (prefix, env string, err error) {
	parts := strings.Split(secret, "_")
	if len(parts) != 4 || parts[0] != apiKeyMarker || (parts[1] != envLive && parts[1] != envTest) ||
		len(parts[2]) != keyPrefixChars || len(parts[3]) != keySecretChars ||
		!isBase62(parts[2]) || !isBase62(parts[3]) {
		return "", "", ErrMalformedKey
	}
	return strings.Join(parts[:3], "_"), parts[1], nil
}

// HashAPIKey returns SHA-256(secret || pepper).
func HashAPIKey(secret string, pepper []byte) []byte {
	h := sha256.New()
	h.Write([]byte(secret))
	h.Write(pepper)
	return h.Sum(nil)
}

func randomBase62(n int) (string, error) {
	var sb strings.Builder
	sb.Grow(n)
	max := big.NewInt(int64(len(base62)))
	for range n {
		i, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("auth: random: %w", err)
		}
		sb.WriteByte(base62[i.Int64()])
	}
	return sb.String(), nil
}

func isBase62(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// Agent tokens (docs/AGENTS.md): kd_agent_<8-char prefix>_<32 base62 chars>.
const agentMarker = "agent"

// GenerateAgentToken creates a token for an AI agent.
func GenerateAgentToken(pepper []byte) (GeneratedKey, error) {
	p, err := randomBase62(keyPrefixChars)
	if err != nil {
		return GeneratedKey{}, err
	}
	s, err := randomBase62(keySecretChars)
	if err != nil {
		return GeneratedKey{}, err
	}
	prefix := apiKeyMarker + "_" + agentMarker + "_" + p
	secret := prefix + "_" + s
	return GeneratedKey{Secret: secret, Prefix: prefix, Hash: HashAPIKey(secret, pepper)}, nil
}

// IsAgentToken reports whether s looks like an agent token (shape only).
func IsAgentToken(s string) bool { return strings.HasPrefix(s, apiKeyMarker+"_"+agentMarker+"_") }

// ParseAgentToken validates the shape of an agent token and returns its prefix.
func ParseAgentToken(secret string) (prefix string, err error) {
	parts := strings.Split(secret, "_")
	if len(parts) != 4 || parts[0] != apiKeyMarker || parts[1] != agentMarker ||
		len(parts[2]) != keyPrefixChars || len(parts[3]) != keySecretChars ||
		!isBase62(parts[2]) || !isBase62(parts[3]) {
		return "", ErrMalformedKey
	}
	return strings.Join(parts[:3], "_"), nil
}
