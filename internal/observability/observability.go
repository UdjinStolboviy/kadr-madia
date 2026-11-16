// Package observability provides logging, request ids, access logs, health
// endpoints and Prometheus metrics.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
)

// sensitiveKeys are redacted from log attributes regardless of nesting.
var sensitiveKeys = map[string]bool{
	"password": true, "secret": true, "secret_access_key": true, "token": true,
	"authorization": true, "cookie": true, "csrf_token": true, "totp_code": true,
}

// NewLogger returns a JSON logger (text in dev) that redacts secrets.
func NewLogger(w io.Writer, level slog.Level, dev bool) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if sensitiveKeys[strings.ToLower(a.Key)] {
				return slog.String(a.Key, "[REDACTED]")
			}
			return a
		},
	}
	var h slog.Handler
	if dev {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(&ctxHandler{Handler: h})
}

// ctxHandler adds request_id from the context to every record.
type ctxHandler struct{ slog.Handler }

func (h *ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *ctxHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &ctxHandler{h.Handler.WithAttrs(as)}
}

func (h *ctxHandler) WithGroup(name string) slog.Handler {
	return &ctxHandler{h.Handler.WithGroup(name)}
}

type requestIDKey struct{}

// RequestIDFrom returns the request id stored in ctx.
func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey{}).(string)
	return s
}

// RequestID accepts a sane incoming X-Request-Id or generates one, stores it
// in the context and echoes it in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID(id) {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// AccessLog logs one line per request and records HTTP metrics in set.
// server labels the metrics ("admin" or "public").
func AccessLog(log *slog.Logger, set *metrics.Set, server string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			dur := time.Since(start)
			if set != nil {
				set.GetOrCreateCounter(fmt.Sprintf(`kadr_http_requests_total{server=%q,code="%d"}`, server, status)).Inc()
				set.GetOrCreateHistogram(fmt.Sprintf(`kadr_http_request_duration_seconds{server=%q}`, server)).Update(dur.Seconds())
			}
			lvl := slog.LevelInfo
			if status >= 500 {
				lvl = slog.LevelError
			} else if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				lvl = slog.LevelDebug
			}
			log.LogAttrs(r.Context(), lvl, "http request",
				slog.String("server", server),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Int64("duration_ms", dur.Milliseconds()),
				slog.String("ip", httpx.ClientIP(r)),
			)
		})
	}
}

// Recoverer turns panics into 500 problem responses and logs the stack.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.ErrorContext(r.Context(), "panic", "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
					httpx.WriteProblem(w, http.StatusInternalServerError, "internal", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Healthz reports liveness.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Pinger is anything that can check readiness (the database).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Readyz reports readiness: 200 when every pinger succeeds, 503 otherwise.
func Readyz(log *slog.Logger, checks map[string]Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		out := map[string]string{}
		ok := true
		for name, p := range checks {
			if err := p.Ping(ctx); err != nil {
				log.WarnContext(ctx, "readiness check failed", "check", name, "err", err)
				out[name] = "fail"
				ok = false
			} else {
				out[name] = "ok"
			}
		}
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		httpx.WriteJSON(w, status, map[string]any{"status": map[bool]string{true: "ok", false: "fail"}[ok], "checks": out})
	}
}

// MetricsHandler writes the metric set plus Go/process metrics.
func MetricsHandler(set *metrics.Set) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		set.WritePrometheus(w)
		metrics.WriteProcessMetrics(w)
	}
}
