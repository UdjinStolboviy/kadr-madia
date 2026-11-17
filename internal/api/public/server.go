// Package public implements the LAN-only API listener: health, metrics and
// the /v1 project API (docs/API.md, api/openapi.yaml).
package public

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/observability"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

// Config holds the dependencies of the public server.
type Config struct {
	Store    *db.Store
	Verifier *auth.KeyVerifier
	Logger   *slog.Logger
	Metrics  *metrics.Set
	// Pipeline serves the media endpoints. When nil only /v1/ping is
	// available and every other /v1 route returns 501.
	Pipeline *pipeline.Service
	// TmpDir is used to spool large idempotent request bodies.
	TmpDir string
	Now    func() time.Time
}

// maxMultipartOverhead is allowed on top of the file in POST /v1/assets.
const maxMultipartOverhead = 1 << 20

// Handler builds the public listener handler.
func Handler(cfg Config) http.Handler {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TmpDir == "" {
		cfg.TmpDir = os.TempDir()
	}
	r := chi.NewRouter()
	r.Use(observability.RequestID)
	r.Use(observability.Recoverer(log))
	r.Use(observability.AccessLog(log, cfg.Metrics, "public"))

	r.Get("/healthz", observability.Healthz)
	r.Get("/readyz", observability.Readyz(log, map[string]observability.Pinger{"db": cfg.Store.DB()}))
	r.Get("/metrics", observability.MetricsHandler(cfg.Metrics))

	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.APIKeyMiddleware(cfg.Verifier, log))
		r.Get("/ping", handlePing)
		if cfg.Pipeline != nil {
			h := &handlers{svc: cfg.Pipeline, log: log}
			spool := filepath.Join(cfg.TmpDir, "spool")
			r.Group(func(r chi.Router) {
				r.Use(idempotency(cfg.Store, spool, pipeline.MaxServerUploadBytes+maxMultipartOverhead, cfg.Now, log))
				h.routes(r)
			})
		}
		r.NotFound(notImplemented)
		r.MethodNotAllowed(notImplemented)
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return r
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"project":     p.ProjectSlug,
		"project_id":  p.ProjectID,
		"environment": p.Environment,
		"key_id":      p.KeyID,
		"scopes":      p.Scopes,
	})
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteProblem(w, http.StatusNotImplemented, "not_implemented", "this endpoint is not available yet")
}
