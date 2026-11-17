package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

const (
	idempotencyHeader = "Idempotency-Key"
	replayedHeader    = "Idempotent-Replayed"
	maxKeyLen         = 255
	// memSpoolLimit: request bodies up to this size are hashed in memory,
	// larger ones (multipart uploads) are spooled to a temporary file.
	memSpoolLimit = 1 << 20
)

// idempotency implements Idempotency-Key for POST requests (kept 24 h by
// the retention job):
//
//   - first request with a key: the key is reserved, the handler runs and a
//     2xx response is stored; any other outcome releases the key so the
//     request can be retried;
//   - same key, same request hash, stored response: the response is replayed
//     with "Idempotent-Replayed: true";
//   - same key while the first request is still running: 409
//     idempotency_in_progress;
//   - same key, different method/path/query/body: 422 idempotency_mismatch.
//
// The request hash covers the method, path, query and the raw body bytes; a
// retried multipart request must resend the identical body (same boundary).
func idempotency(store *db.Store, tmpDir string, maxBody int64, now func() time.Time, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(idempotencyHeader)
			if r.Method != http.MethodPost || key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !validIdempotencyKey(key) {
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_idempotency_key",
					"Idempotency-Key must be 1–255 printable ASCII characters")
				return
			}
			p, ok := auth.PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "")
				return
			}
			body, hash, cleanup, err := spoolBody(r, tmpDir, maxBody)
			defer cleanup()
			if errors.Is(err, errBodyTooLarge) {
				httpx.WriteProblem(w, http.StatusRequestEntityTooLarge, "too_large", "request body is too large")
				return
			}
			if err != nil {
				log.ErrorContext(r.Context(), "spool request body", "err", err)
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_body", "could not read the request body")
				return
			}
			rec, created, err := store.ReserveIdempotencyKey(r.Context(), p.ProjectID, key, hash, now())
			if err != nil {
				log.ErrorContext(r.Context(), "reserve idempotency key", "err", err)
				httpx.WriteProblem(w, http.StatusInternalServerError, "internal", "")
				return
			}
			if !created {
				switch {
				case !bytes.Equal(rec.RequestHash, hash):
					httpx.WriteProblem(w, http.StatusUnprocessableEntity, "idempotency_mismatch",
						"this Idempotency-Key was already used with a different request")
				case rec.ResponseCode == 0:
					httpx.WriteProblem(w, http.StatusConflict, "idempotency_in_progress",
						"a request with this Idempotency-Key is still being processed")
				default:
					w.Header().Set(replayedHeader, "true")
					w.Header().Set("Cache-Control", "no-store")
					if rec.ResponseBody != "" {
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
					}
					w.WriteHeader(rec.ResponseCode)
					_, _ = io.WriteString(w, rec.ResponseBody)
				}
				return
			}

			stored := false
			defer func() {
				if !stored {
					// Handler failed or panicked: release the key.
					if err := store.DeleteIdempotencyKey(context.WithoutCancel(r.Context()), p.ProjectID, key); err != nil {
						log.ErrorContext(r.Context(), "release idempotency key", "err", err)
					}
				}
			}()
			r.Body = body
			cw := &captureWriter{ResponseWriter: w}
			next.ServeHTTP(cw, r)
			if cw.status == 0 {
				cw.status = http.StatusOK
			}
			if cw.status/100 == 2 {
				if err := store.CompleteIdempotencyKey(context.WithoutCancel(r.Context()), p.ProjectID, key, cw.status, cw.buf.String()); err != nil {
					log.ErrorContext(r.Context(), "store idempotent response", "err", err)
					return
				}
				stored = true
			}
		})
	}
}

func validIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > maxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

var errBodyTooLarge = errors.New("request body too large")

// spoolBody reads the body (in memory or into a temp file), returning a
// replacement reader and the request hash.
func spoolBody(r *http.Request, tmpDir string, maxBody int64) (io.ReadCloser, []byte, func(), error) {
	h := sha256.New()
	h.Write([]byte(r.Method + "\n" + r.URL.Path + "?" + r.URL.RawQuery + "\n"))
	noop := func() {}
	if r.Body == nil || r.Body == http.NoBody {
		return http.NoBody, h.Sum(nil), noop, nil
	}
	defer r.Body.Close()
	limited := io.LimitReader(r.Body, maxBody+1)
	if r.ContentLength >= 0 && r.ContentLength <= memSpoolLimit {
		b, err := io.ReadAll(limited)
		if err != nil {
			return nil, nil, noop, err
		}
		if int64(len(b)) > maxBody {
			return nil, nil, noop, errBodyTooLarge
		}
		h.Write(b)
		return io.NopCloser(bytes.NewReader(b)), h.Sum(nil), noop, nil
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, nil, noop, err
	}
	f, err := os.CreateTemp(tmpDir, "idem-*")
	if err != nil {
		return nil, nil, noop, err
	}
	cleanup := func() { f.Close(); os.Remove(f.Name()) }
	n, err := io.Copy(io.MultiWriter(f, h), limited)
	if err != nil {
		return nil, nil, cleanup, err
	}
	if n > maxBody {
		return nil, nil, cleanup, errBodyTooLarge
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, nil, cleanup, err
	}
	return io.NopCloser(f), h.Sum(nil), cleanup, nil
}

// captureWriter tees the response so it can be stored.
type captureWriter struct {
	http.ResponseWriter
	status int
	buf    strings.Builder
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.buf.Len() < memSpoolLimit {
		c.buf.Write(b)
	}
	return c.ResponseWriter.Write(b)
}
