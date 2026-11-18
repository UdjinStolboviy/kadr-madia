// Package cfaccess verifies Cloudflare Access application tokens
// (the Cf-Access-Jwt-Assertion header) on the admin listener, so that a
// request reaching Kadr without passing Cloudflare Access is refused even if
// the origin is exposed some other way (docs/SECURITY.md §3).
//
// Tokens are RS256 JWTs signed by the team's keys published at
// https://<team>.cloudflareaccess.com/cdn-cgi/access/certs.
package cfaccess

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Header carries the Access token on proxied requests.
const Header = "Cf-Access-Jwt-Assertion"

// Errors returned by Verify.
var (
	ErrMissing = errors.New("cfaccess: missing token")
	ErrInvalid = errors.New("cfaccess: invalid token")
)

// Claims are the verified token fields Kadr uses.
type Claims struct {
	Email    string
	Subject  string
	Audience []string
	Expiry   time.Time
	// CommonName is set for service tokens (no email).
	CommonName string
}

// Verifier checks Access tokens against the team's signing keys.
type Verifier struct {
	TeamDomain string   // e.g. myteam.cloudflareaccess.com
	Audiences  []string // Application Audience (AUD) tags
	Client     *http.Client
	Now        func() time.Time
	Logger     *slog.Logger

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

const keysTTL = time.Hour

func (v *Verifier) issuer() string {
	return "https://" + strings.TrimSuffix(strings.TrimPrefix(v.TeamDomain, "https://"), "/")
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify validates a token: signature, issuer, audience, expiry.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	if token == "" {
		return nil, ErrMissing
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalid
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil || hdr.Alg != "RS256" {
		return nil, ErrInvalid
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return nil, ErrInvalid
	}
	var raw struct {
		Iss    string          `json:"iss"`
		Aud    json.RawMessage `json:"aud"`
		Exp    int64           `json:"exp"`
		Nbf    int64           `json:"nbf"`
		Email  string          `json:"email"`
		Sub    string          `json:"sub"`
		Common string          `json:"common_name"`
	}
	if err := decodeSegment(parts[1], &raw); err != nil {
		return nil, ErrInvalid
	}
	now := v.now()
	if raw.Iss != v.issuer() || raw.Exp == 0 || now.After(time.Unix(raw.Exp, 0).Add(time.Minute)) ||
		(raw.Nbf != 0 && now.Add(time.Minute).Before(time.Unix(raw.Nbf, 0))) {
		return nil, ErrInvalid
	}
	var aud []string
	if err := json.Unmarshal(raw.Aud, &aud); err != nil {
		var one string
		if json.Unmarshal(raw.Aud, &one) != nil {
			return nil, ErrInvalid
		}
		aud = []string{one}
	}
	ok := false
	for _, a := range aud {
		if slices.Contains(v.Audiences, a) {
			ok = true
		}
	}
	if !ok {
		return nil, ErrInvalid
	}
	return &Claims{Email: raw.Email, Subject: raw.Sub, Audience: aud, Expiry: time.Unix(raw.Exp, 0), CommonName: raw.Common}, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// key returns the signing key kid, refreshing the key set when it is stale
// or the kid is unknown (key rotation), at most once a minute.
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if k, ok := v.keys[kid]; ok && now.Sub(v.fetched) < keysTTL {
		return k, nil
	}
	if v.keys == nil || now.Sub(v.fetched) >= time.Minute {
		keys, err := v.fetch(ctx)
		if err != nil {
			if k, ok := v.keys[kid]; ok { // keep serving the cached key if Cloudflare is unreachable
				if v.Logger != nil {
					v.Logger.Warn("cloudflare access certs refresh failed; using cached keys", "err", err)
				}
				return k, nil
			}
			return nil, fmt.Errorf("cfaccess: fetch certs: %w", err)
		}
		v.keys, v.fetched = keys, now
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrInvalid
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	c := v.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.issuer()+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, err
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(out) == 0 {
		return nil, errors.New("no RSA keys in the certs response")
	}
	return out, nil
}

// Middleware refuses requests without a valid Access token, except for the
// exempt path prefixes (health checks and the paths published with an Access
// Bypass policy, e.g. /mcp for AI clients that authenticate with their own
// tokens).
func (v *Verifier) Middleware(exempt []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range exempt {
				if r.URL.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(r.URL.Path, p)) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if _, err := v.Verify(r.Context(), r.Header.Get(Header)); err != nil {
				if v.Logger != nil && !errors.Is(err, ErrMissing) && !errors.Is(err, ErrInvalid) {
					v.Logger.Error("cloudflare access verification failed", "err", err)
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"type":"about:blank","title":"Forbidden","status":403,"code":"access_required","detail":"this endpoint is only reachable through Cloudflare Access"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
