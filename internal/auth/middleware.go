package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Principal is the authenticated caller of the public /v1 API.
type Principal struct {
	KeyID       string
	ProjectID   string
	ProjectSlug string
	Environment string
	Scopes      []string
	Namespaces  []string
}

// AllowsNamespace reports whether the principal may access ns (empty Namespaces = unrestricted).
func (p *Principal) AllowsNamespace(ns string) bool {
	return len(p.Namespaces) == 0 || slices.Contains(p.Namespaces, ns)
}

// HasScope reports whether the principal holds scope.
func (p *Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored by the API key middleware.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// Verification errors. They are deliberately not distinguished in responses.
var (
	ErrKeyInvalid = errors.New("auth: invalid api key")
	ErrKeyRevoked = errors.New("auth: api key revoked")
	ErrKeyExpired = errors.New("auth: api key expired")
	ErrProjectOff = errors.New("auth: project disabled")
)

// KeyVerifier checks API key secrets against the database.
type KeyVerifier struct {
	Store  *db.Store
	Pepper []byte
	Now    func() time.Time
}

// Verify resolves a secret to a principal.
func (v *KeyVerifier) Verify(ctx context.Context, secret string) (*Principal, error) {
	prefix, env, err := ParseAPIKey(secret)
	if err != nil {
		return nil, ErrKeyInvalid
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	k, err := v.Store.GetAPIKeyByPrefix(ctx, prefix)
	if errors.Is(err, db.ErrNotFound) {
		// Hash anyway to keep timing uniform.
		_ = HashAPIKey(secret, v.Pepper)
		return nil, ErrKeyInvalid
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(HashAPIKey(secret, v.Pepper), k.SecretHash) != 1 {
		return nil, ErrKeyInvalid
	}
	if k.RevokedAt != nil {
		return nil, ErrKeyRevoked
	}
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return nil, ErrKeyExpired
	}
	p, err := v.Store.GetProject(ctx, k.ProjectID)
	if err != nil {
		return nil, err
	}
	if p.DisabledAt != nil {
		return nil, ErrProjectOff
	}
	if p.Environment != env {
		return nil, ErrKeyInvalid
	}
	if err := v.Store.TouchAPIKey(ctx, k.ID, now, time.Minute); err != nil {
		// Non-fatal: the request is still authenticated.
		slog.WarnContext(ctx, "touch api key failed", "key_id", k.ID, "err", err)
	}
	return &Principal{KeyID: k.ID, ProjectID: p.ID, ProjectSlug: p.Slug, Environment: p.Environment, Scopes: k.Scopes, Namespaces: k.Namespaces}, nil
}

// APIKeyMiddleware authenticates requests with "Authorization: Bearer kd_…".
func APIKeyMiddleware(v *KeyVerifier, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			secret, ok := strings.CutPrefix(h, "Bearer ")
			if !ok || secret == "" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="kadr"`)
				httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "missing bearer API key")
				return
			}
			p, err := v.Verify(r.Context(), strings.TrimSpace(secret))
			if err != nil {
				switch {
				case errors.Is(err, ErrKeyInvalid), errors.Is(err, ErrKeyRevoked), errors.Is(err, ErrKeyExpired):
					log.InfoContext(r.Context(), "api key rejected", "reason", err.Error())
					w.Header().Set("WWW-Authenticate", `Bearer realm="kadr", error="invalid_token"`)
					httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "invalid API key")
				case errors.Is(err, ErrProjectOff):
					httpx.WriteProblem(w, http.StatusForbidden, "project_disabled", "project is disabled")
				default:
					log.ErrorContext(r.Context(), "api key verification failed", "err", err)
					httpx.WriteProblem(w, http.StatusInternalServerError, "internal", "")
				}
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// RequireScope rejects requests whose principal lacks scope with 403.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "")
				return
			}
			if !p.HasScope(scope) {
				httpx.WriteProblem(w, http.StatusForbidden, "insufficient_scope", "API key lacks scope "+scope)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
