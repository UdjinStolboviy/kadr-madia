package public

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

type handlers struct {
	svc *pipeline.Service
	log *slog.Logger
}

func (h *handlers) routes(r chi.Router) {
	upload := auth.RequireScope(auth.ScopeUploadsWrite)
	read := auth.RequireScope(auth.ScopeAssetsRead)

	r.With(upload).Post("/uploads", h.createUpload)
	r.With(upload).Get("/uploads/{id}", h.getUpload)
	r.With(upload).Post("/uploads/{id}/parts", h.refreshUploadParts)
	r.With(upload).Post("/uploads/{id}/complete", h.completeUpload)
	r.With(upload).Delete("/uploads/{id}", h.abortUpload)

	r.With(upload).Post("/assets", h.createServerAsset)
	r.With(read).Get("/assets", h.listAssets)
	r.With(read).Get("/assets/{id}", h.getAsset)
	r.With(auth.RequireScope(auth.ScopeAssetsWrite)).Patch("/assets/{id}", h.patchAsset)
	r.With(auth.RequireScope(auth.ScopeAssetsDelete)).Delete("/assets/{id}", h.deleteAsset)

	imp := auth.RequireScope(auth.ScopeImportsWrite)
	r.With(imp).Post("/imports/url", h.importURL)
	r.With(imp).Post("/imports/cloud", h.importCloud)
	r.With(auth.RequireScope(auth.ScopeURLsSign)).Post("/signed-urls", h.signURLs)

	r.Get("/presets", h.listPresets)
	r.Get("/presets/{slug}", h.getPreset)

	r.With(requireAnyScope(auth.ScopeAssetsRead, auth.ScopeNamespacesAdmin)).Get("/namespaces/{ns}/usage", h.namespaceUsage)
	r.With(auth.RequireScope(auth.ScopeNamespacesAdmin)).Put("/namespaces/{ns}/quota", h.setQuota)
}

// requireAnyScope passes when the principal holds at least one of scopes.
func requireAnyScope(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := auth.PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "")
				return
			}
			for _, s := range scopes {
				if p.HasScope(s) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.WriteProblem(w, http.StatusForbidden, "insufficient_scope", "API key lacks one of the scopes "+strings.Join(scopes, ", "))
		})
	}
}

func projectID(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.ProjectID
}

// fail renders pipeline errors as problem+json and hides everything else.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	var pe *pipeline.Error
	if errors.As(err, &pe) {
		httpx.WriteProblemErrors(w, pe.Status, pe.Code, pe.Detail, pe.Fields)
		return
	}
	if r.Context().Err() != nil {
		h.log.InfoContext(r.Context(), "request cancelled", "path", r.URL.Path, "err", err)
		return
	}
	h.log.ErrorContext(r.Context(), "api error", "path", r.URL.Path, "err", err)
	httpx.WriteProblem(w, http.StatusInternalServerError, "internal", "internal server error")
}

func badJSON(w http.ResponseWriter, err error) {
	httpx.WriteProblem(w, http.StatusBadRequest, "invalid_json", err.Error())
}

func parseWait(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	d, err := pipeline.ParseWait(r.URL.Query().Get("wait"))
	if err != nil {
		httpx.WriteProblemErrors(w, http.StatusUnprocessableEntity, "validation_failed", "request validation failed",
			map[string]string{"wait": err.Error()})
		return 0, false
	}
	return d, true
}

// ───────────── uploads ─────────────

func (h *handlers) createUpload(w http.ResponseWriter, r *http.Request) {
	var in pipeline.CreateUploadInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	res, err := h.svc.CreateUpload(r.Context(), projectID(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if res.Asset != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"asset": res.Asset})
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res.Ticket)
}

func (h *handlers) getUpload(w http.ResponseWriter, r *http.Request) {
	u, err := h.svc.GetUpload(r.Context(), projectID(r), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (h *handlers) refreshUploadParts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PartNumbers []int `json:"part_numbers"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &body); err != nil && !errors.Is(err, io.EOF) {
			badJSON(w, err)
			return
		}
	}
	out, err := h.svc.RefreshUploadParts(r.Context(), projectID(r), chi.URLParam(r, "id"), body.PartNumbers)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) completeUpload(w http.ResponseWriter, r *http.Request) {
	wait, ok := parseWait(w, r)
	if !ok {
		return
	}
	var body struct {
		Parts []pipeline.CompletedPartInput `json:"parts"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &body); err != nil && !errors.Is(err, io.EOF) {
			badJSON(w, err)
			return
		}
	}
	a, err := h.svc.CompleteUpload(r.Context(), projectID(r), chi.URLParam(r, "id"), body.Parts, wait)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func (h *handlers) abortUpload(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.AbortUpload(r.Context(), projectID(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ───────────── assets ─────────────

// createServerAsset handles POST /v1/assets (multipart/form-data) with the
// fields preset, namespace, visibility, metadata (JSON object) and file. The
// parts may come in any order; the file is streamed to disk.
func (h *handlers) createServerAsset(w http.ResponseWriter, r *http.Request) {
	wait, ok := parseWait(w, r)
	if !ok {
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		httpx.WriteProblem(w, http.StatusUnsupportedMediaType, "unsupported_content_type", "POST /v1/assets expects multipart/form-data")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pipeline.MaxServerUploadBytes+maxMultipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", err.Error())
		return
	}
	var in pipeline.ServerUploadInput
	var staged *pipeline.Staged
	defer func() { staged.Discard() }()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				httpx.WriteProblem(w, http.StatusRequestEntityTooLarge, pipeline.CodeTooLarge, "server-side uploads are limited to 25 MB")
				return
			}
			httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", err.Error())
			return
		}
		switch part.FormName() {
		case "file":
			if staged != nil {
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", "exactly one file part is allowed")
				return
			}
			in.Filename = part.FileName()
			in.ContentType = part.Header.Get("Content-Type")
			if staged, err = h.svc.StageServerUpload(part); err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					httpx.WriteProblem(w, http.StatusRequestEntityTooLarge, pipeline.CodeTooLarge, "server-side uploads are limited to 25 MB")
					return
				}
				h.fail(w, r, err)
				return
			}
		case "preset", "mode", "namespace", "visibility", "metadata", "filename":
			v, err := io.ReadAll(io.LimitReader(part, pipeline.MaxMetadataBytes+1))
			if err != nil {
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", err.Error())
				return
			}
			switch part.FormName() {
			case "preset":
				in.Preset = string(v)
			case "mode":
				in.Mode = string(v)
			case "namespace":
				in.Namespace = string(v)
			case "visibility":
				in.Visibility = string(v)
			case "metadata":
				in.Metadata = json.RawMessage(v)
			case "filename":
				in.Filename = string(v)
			}
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, pipeline.MaxMetadataBytes))
		}
		part.Close()
	}
	a, err := h.svc.CreateServerAsset(r.Context(), projectID(r), in, staged, wait)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, a)
}

func (h *handlers) listAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := pipeline.ListAssetsInput{Namespace: q.Get("namespace"), Kind: q.Get("kind"), Status: q.Get("status"), Cursor: q.Get("cursor")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpx.WriteProblemErrors(w, http.StatusUnprocessableEntity, "validation_failed", "request validation failed",
				map[string]string{"limit": "must be an integer"})
			return
		}
		in.Limit = n
	}
	page, err := h.svc.ListAssets(r.Context(), projectID(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *handlers) getAsset(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.GetAsset(r.Context(), projectID(r), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func (h *handlers) patchAsset(w http.ResponseWriter, r *http.Request) {
	var in pipeline.PatchAssetInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	a, err := h.svc.PatchAsset(r.Context(), projectID(r), chi.URLParam(r, "id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func (h *handlers) deleteAsset(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteAsset(r.Context(), projectID(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ───────────── presets, namespaces ─────────────

func (h *handlers) listPresets(w http.ResponseWriter, r *http.Request) {
	ps, err := h.svc.ListPresets(r.Context(), projectID(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (h *handlers) getPreset(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPreset(r.Context(), projectID(r), chi.URLParam(r, "slug"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *handlers) namespaceUsage(w http.ResponseWriter, r *http.Request) {
	u, err := h.svc.NamespaceUsage(r.Context(), projectID(r), chi.URLParam(r, "ns"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (h *handlers) setQuota(w http.ResponseWriter, r *http.Request) {
	var in pipeline.QuotaInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	u, err := h.svc.SetNamespaceQuota(r.Context(), projectID(r), chi.URLParam(r, "ns"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// signURLs handles POST /v1/signed-urls.
func (h *handlers) signURLs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []pipeline.SignItem `json:"items"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	out, err := h.svc.SignURLs(r.Context(), projectID(r), in.Items)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// importURL handles POST /v1/imports/url → 202 { asset (processing) }.
func (h *handlers) importURL(w http.ResponseWriter, r *http.Request) {
	var in pipeline.ImportURLInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	a, err := h.svc.ImportURL(r.Context(), projectID(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, a)
}

// importCloud handles POST /v1/imports/cloud → 202.
func (h *handlers) importCloud(w http.ResponseWriter, r *http.Request) {
	var in pipeline.ImportCloudInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	a, err := h.svc.ImportCloud(r.Context(), projectID(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, a)
}
