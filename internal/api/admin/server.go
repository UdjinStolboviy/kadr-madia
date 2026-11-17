// Package admin implements the Admin API (/admin/api, docs/ADMIN_API.md) and
// serves the embedded admin UI on the admin listener.
package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/capabilities"
	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/edgepublish"
	"github.com/UdjinStolboviy/kadr-madia/internal/observability"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version   string
	Commit    string
	StartedAt time.Time
}

// Config holds the dependencies of the admin server.
type Config struct {
	Store   *db.Store
	Keyring *crypto.Keyring
	Logger  *slog.Logger
	Metrics *metrics.Set
	Dev     bool
	Pepper  []byte
	Build   BuildInfo
	Caps    *capabilities.Holder
	TmpDir  string

	// UI is the built admin UI (nil → FallbackHTML is served).
	UI           fs.FS
	FallbackHTML []byte

	// NewDriver builds a storage driver for profile tests (default: storage.NewS3).
	NewDriver func(storage.Config) (storage.Driver, error)
	// HTTPClient is used by storage tests for presigned PUT and public GET.
	HTTPClient *http.Client
	// Webhooks delivers test events and wakes up on manual retries
	// (optional; the test endpoint returns 503 without it).
	Webhooks *webhook.Dispatcher
	// Edge publishes signing keys to kadr-edge (optional).
	Edge *edgepublish.Publisher
	// Playground runs presets on samples (optional; nil → 503).
	Playground PlaygroundEngine
	// Pipeline backs the asset explorer (optional).
	Pipeline AssetService
	// Wake nudges the job pool of a requeued job (optional).
	Wake func(pool string)
	// WebAuthnRPID / WebAuthnOrigins configure passkeys (empty → disabled, 503).
	WebAuthnRPID    string
	WebAuthnOrigins []string
	// Now overrides the clock (tests).
	Now func() time.Time
}

// AssetService is the part of the media pipeline used by the asset explorer.
type AssetService interface {
	ListAssets(ctx context.Context, projectID string, in pipeline.ListAssetsInput) (*pipeline.AssetPage, error)
	GetAsset(ctx context.Context, projectID, id string) (*pipeline.AssetView, error)
	DeleteAsset(ctx context.Context, projectID, id string) error
}

// Server is the admin HTTP handler.
type Server struct {
	cfg        Config
	log        *slog.Logger
	store      *db.Store
	loginLimit *auth.RateLimiter // per IP + email
	ipLimit    *auth.RateLimiter // per IP, catches email spraying
	cookieName string
	playSem    chan struct{}
	wa         *webAuthnState
}

// Session cookie names (docs/ADMIN_API.md).
const (
	cookieProd = "__Host-kadr_session"
	cookieDev  = "kadr_session"
)

// New builds the admin server.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewDriver == nil {
		cfg.NewDriver = func(c storage.Config) (storage.Driver, error) { return storage.NewS3(c) }
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("too many redirects")
				}
				return nil
			},
		}
	}
	if cfg.Caps == nil {
		cfg.Caps = capabilities.NewHolder()
	}
	name := cookieProd
	if cfg.Dev {
		name = cookieDev
	}
	return &Server{
		cfg:        cfg,
		log:        cfg.Logger,
		store:      cfg.Store,
		loginLimit: auth.NewRateLimiter(5, time.Minute),
		ipLimit:    auth.NewRateLimiter(30, time.Minute),
		cookieName: name,
		playSem:    make(chan struct{}, 2),
		wa:         newWebAuthnState(cfg),
	}
}

// Handler returns the full admin listener handler: health endpoints, the
// Admin API and the SPA.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(observability.RequestID)
	r.Use(observability.Recoverer(s.log))
	r.Use(observability.AccessLog(s.log, s.cfg.Metrics, "admin"))
	r.Use(securityHeaders(buildCSP(s.cfg.UI)))

	r.Get("/healthz", observability.Healthz)
	r.Get("/readyz", observability.Readyz(s.log, map[string]observability.Pinger{"db": s.store.DB()}))
	r.Mount("/admin/api", s.apiRouter())

	spa := newSPAHandler(s.cfg.UI, s.cfg.FallbackHTML)
	r.Get("/*", spa.ServeHTTP)
	r.Head("/*", spa.ServeHTTP)
	return r
}

func (s *Server) apiRouter() http.Handler {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	})

	r.Post("/auth/login", s.handleLogin)
	r.Post("/auth/webauthn/login/begin", s.handleWebAuthnLoginBegin)
	r.Post("/auth/webauthn/login/finish", s.handleWebAuthnLoginFinish)

	r.Group(func(r chi.Router) {
		r.Use(s.requireSession)
		r.Use(s.requireCSRF)
		r.Use(s.requireWriteRole)

		r.Post("/auth/logout", s.handleLogout)
		r.Get("/auth/me", s.handleMe)
		r.Post("/auth/totp/setup", s.handleTOTPSetup)
		r.Post("/auth/totp/enable", s.handleTOTPEnable)
		r.Post("/auth/totp/disable", s.handleTOTPDisable)
		r.Post("/auth/webauthn/register/begin", s.handleWebAuthnRegisterBegin)
		r.Post("/auth/webauthn/register/finish", s.handleWebAuthnRegisterFinish)
		r.Get("/auth/webauthn/credentials", s.handleWebAuthnList)
		r.Delete("/auth/webauthn/credentials/{id}", s.handleWebAuthnDelete)

		r.Get("/projects", s.handleListProjects)
		r.Post("/projects", s.handleCreateProject)
		r.Get("/projects/{id}", s.handleGetProject)
		r.Patch("/projects/{id}", s.handlePatchProject)
		r.Get("/projects/{id}/api-keys", s.handleListAPIKeys)
		r.Post("/projects/{id}/api-keys", s.handleCreateAPIKey)
		r.Get("/projects/{id}/presets", s.handleListPresets)
		r.Delete("/api-keys/{key_id}", s.handleRevokeAPIKey)
		r.Get("/projects/{id}/webhooks", s.handleListWebhooks)
		r.Post("/projects/{id}/webhooks", s.handleCreateWebhook)
		r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
		r.Post("/webhooks/{id}/test", s.handleTestWebhook)
		r.Get("/projects/{id}/deliveries", s.handleListDeliveries)
		r.Post("/deliveries/{id}/retry", s.handleRetryDelivery)

		r.Get("/storage-profiles", s.handleListStorageProfiles)
		r.Post("/storage-profiles", s.handleCreateStorageProfile)
		r.Get("/storage-profiles/{id}", s.handleGetStorageProfile)
		r.Patch("/storage-profiles/{id}", s.handlePatchStorageProfile)
		r.Delete("/storage-profiles/{id}", s.handleDeleteStorageProfile)
		r.Post("/storage-profiles/{id}/test", s.handleTestStorageProfile)

		r.Get("/projects/{id}/assets", s.handleListAssets)
		r.Get("/projects/{id}/assets/{asset_id}", s.handleGetAsset)
		r.Delete("/projects/{id}/assets/{asset_id}", s.handleDeleteAsset)
		r.Post("/playground", s.handlePlayground)
		r.Post("/projects/{id}/presets/{slug}/versions", s.handleSavePresetVersion)
		r.Post("/projects/{id}/presets/{slug}/reset", s.handleResetPreset)
		r.Get("/projects/{id}/modes", s.handleListModes)
		r.Get("/projects/{id}/analytics", s.handleAnalytics)
		r.Get("/projects/{id}/billing", s.handleBilling)
		r.Get("/billing/rates", s.handleGetBillingRates)
		r.Put("/billing/rates", s.handlePutBillingRates)
		r.Get("/projects/{id}/signing-keys", s.handleListSigningKeys)
		r.Post("/projects/{id}/signing-keys", s.handleCreateSigningKey)
		r.Delete("/signing-keys/{kid}", s.handleRetireSigningKey)
		r.Get("/signing-keys/edge-config", s.handleEdgeConfig)
		r.Post("/signing-keys/publish", s.handlePublishSigningKeys)
		r.Get("/projects/{id}/cloud-connections", s.handleListCloudConnections)
		r.Post("/projects/{id}/cloud-connections", s.handleCreateCloudConnection)
		r.Delete("/cloud-connections/{id}", s.handleRevokeCloudConnection)
		r.Get("/jobs", s.handleListJobs)
		r.Get("/jobs/stream", s.handleJobsStream)
		r.Post("/jobs/{id}/retry", s.handleRetryJob)
		r.Post("/jobs/{id}/cancel", s.handleCancelJob)

		r.Get("/system", s.handleSystem)
		r.Get("/audit", s.handleListAudit)
	})
	return r
}

// securityHeaders sets conservative headers for both the API and the SPA.
func securityHeaders(csp string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Content-Security-Policy", csp)
			next.ServeHTTP(w, r)
		})
	}
}

var inlineScriptRe = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

// buildCSP returns the Content-Security-Policy for the admin listener. Inline
// <script> blocks of the embedded index.html (e.g. the theme bootstrap) are
// allowed by their SHA-256 hash, computed at startup so the policy follows
// the UI build automatically.
func buildCSP(ui fs.FS) string {
	scriptSrc := []string{"'self'"}
	if ui != nil {
		if b, err := fs.ReadFile(ui, "index.html"); err == nil {
			for _, m := range inlineScriptRe.FindAllSubmatch(b, -1) {
				if bytes.Contains(bytes.ToLower(m[1]), []byte("src=")) || len(bytes.TrimSpace(m[2])) == 0 {
					continue
				}
				sum := sha256.Sum256(m[2])
				scriptSrc = append(scriptSrc, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
			}
		}
	}
	return "default-src 'self'; img-src 'self' data: blob: https:; media-src 'self' blob: https:; " +
		"style-src 'self' 'unsafe-inline'; script-src " + strings.Join(scriptSrc, " ") + "; connect-src 'self'; " +
		"font-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
}

// ───────────── request context ─────────────

type ctxKey int

const (
	ctxAdmin ctxKey = iota
	ctxSession
)

func adminFrom(ctx context.Context) *db.Admin {
	a, _ := ctx.Value(ctxAdmin).(*db.Admin)
	return a
}

func sessionFrom(ctx context.Context) *db.Session {
	s, _ := ctx.Value(ctxSession).(*db.Session)
	return s
}

// ───────────── shared helpers ─────────────

func (s *Server) now() time.Time { return s.cfg.Now() }

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "admin api error", "path", r.URL.Path, "err", err)
	httpx.WriteProblem(w, http.StatusInternalServerError, "internal", "internal server error")
}

func badJSON(w http.ResponseWriter, err error) {
	httpx.WriteProblem(w, http.StatusBadRequest, "invalid_json", err.Error())
}

func validationFailed(w http.ResponseWriter, errs map[string]string) {
	httpx.WriteProblemErrors(w, http.StatusUnprocessableEntity, "validation_failed", "request validation failed", errs)
}

func notFound(w http.ResponseWriter, what string) {
	httpx.WriteProblem(w, http.StatusNotFound, "not_found", what+" not found")
}

// audit writes an audit record for the current admin using store (which may
// be a transaction-scoped store).
func (s *Server) audit(r *http.Request, store *db.Store, action string, projectID, target *string, details any) error {
	e := &db.AuditEntry{ActorType: db.ActorAdmin, ProjectID: projectID, Action: action, Target: target, CreatedAt: s.now()}
	if a := adminFrom(r.Context()); a != nil {
		e.ActorID = &a.ID
	}
	ip := httpx.ClientIP(r)
	e.IP = &ip
	return store.InsertAudit(r.Context(), e, details)
}
