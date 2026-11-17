package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// PlaygroundEngine runs presets on sample images and measures the result.
type PlaygroundEngine interface {
	image.Engine
	SSIM(ctx context.Context, srcPath, renditionPath, format string) (float64, error)
}

const (
	playgroundMaxBytes = 25 << 20
	playgroundTimeout  = 60 * time.Second
)

type playgroundRendition struct {
	Name        string  `json:"name"`
	Format      string  `json:"format"`
	ContentType string  `json:"content_type"`
	Bytes       int64   `json:"bytes"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Quality     int     `json:"quality"`
	Iterations  int     `json:"iterations"`
	OverBudget  bool    `json:"over_budget"`
	SSIM        float64 `json:"ssim"`
	Data        string  `json:"data"` // base64 of the encoded file, for Before/After
}

// handlePlayground: POST /admin/api/playground (multipart/form-data: file,
// params = ImageParams JSON). Processes synchronously; nothing is stored.
func (s *Server) handlePlayground(w http.ResponseWriter, r *http.Request) {
	eng := s.cfg.Playground
	if eng == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "the image engine is not available (is libvips installed?)")
		return
	}
	select {
	case s.playSem <- struct{}{}:
		defer func() { <-s.playSem }()
	default:
		httpx.WriteProblem(w, http.StatusTooManyRequests, "rate_limited", "the playground is busy; try again in a moment")
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		httpx.WriteProblem(w, http.StatusUnsupportedMediaType, "unsupported_content_type", "expected multipart/form-data")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, playgroundMaxBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", err.Error())
		return
	}
	dir, err := os.MkdirTemp(s.cfg.TmpDir, "playground-")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer os.RemoveAll(dir)
	srcPath := filepath.Join(dir, "source")
	var params preset.ImageParams
	var haveFile, haveParams bool
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", err.Error())
			return
		}
		switch part.FormName() {
		case "file":
			f, err := os.OpenFile(srcPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			n, cerr := io.Copy(f, io.LimitReader(part, playgroundMaxBytes+1))
			f.Close()
			if cerr != nil {
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_multipart", cerr.Error())
				return
			}
			if n > playgroundMaxBytes {
				httpx.WriteProblem(w, http.StatusRequestEntityTooLarge, "too_large", "the sample is limited to 25 MB")
				return
			}
			haveFile = true
		case "params":
			b, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			if err := json.Unmarshal(b, &params); err != nil {
				validationFailed(w, map[string]string{"params": "must be valid image preset parameters JSON"})
				return
			}
			haveParams = true
		}
	}
	errs := map[string]string{}
	if !haveFile {
		errs["file"] = "required"
	}
	if !haveParams {
		errs["params"] = "required"
	} else if e := validateImageParams(&params); e != "" {
		errs["params"] = e
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	// The playground applies the preset's limits, but never above the sample cap.
	params.Limits.MaxInputBytes = playgroundMaxBytes
	if params.Limits.MaxInputPixels <= 0 || params.Limits.MaxInputPixels > 40_000_000 {
		params.Limits.MaxInputPixels = 40_000_000
	}
	params.RetainOrigin = false

	ctx, cancel := context.WithTimeout(r.Context(), playgroundTimeout)
	defer cancel()
	start := time.Now()
	info, err := eng.Probe(ctx, srcPath, params.Limits)
	if err != nil {
		s.playgroundError(w, r, err)
		return
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := eng.Process(ctx, info, params, outDir)
	if err != nil {
		s.playgroundError(w, r, err)
		return
	}
	res := make([]playgroundRendition, 0, len(out.Renditions))
	for _, x := range out.Renditions {
		b, err := os.ReadFile(x.Path)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		ssim, _ := eng.SSIM(ctx, srcPath, x.Path, x.Format) // best effort
		res = append(res, playgroundRendition{Name: x.Name, Format: x.Format, ContentType: mime.TypeByExtension("." + x.Format),
			Bytes: x.Bytes, Width: x.Width, Height: x.Height, Quality: x.Quality, Iterations: x.Iterations, OverBudget: x.OverBudget,
			SSIM: ssim, Data: base64.StdEncoding.EncodeToString(b)})
		if res[len(res)-1].ContentType == "" {
			res[len(res)-1].ContentType = "image/" + x.Format
		}
	}
	resp := map[string]any{
		"source":     map[string]any{"format": info.Format, "bytes": info.Bytes, "width": info.Width, "height": info.Height},
		"renditions": res, "elapsed_ms": time.Since(start).Milliseconds(), "dominant_color": out.DominantColor,
	}
	if len(out.ThumbHash) > 0 {
		resp["thumbhash"] = base64.StdEncoding.EncodeToString(out.ThumbHash)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) playgroundError(w http.ResponseWriter, r *http.Request, err error) {
	var ie *image.Error
	if errors.As(err, &ie) {
		status := http.StatusUnprocessableEntity
		if ie.Retryable {
			status = http.StatusServiceUnavailable
		}
		httpx.WriteProblem(w, status, ie.Code, ie.Detail)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		httpx.WriteProblem(w, http.StatusGatewayTimeout, "timeout", "processing took too long")
		return
	}
	s.internalError(w, r, err)
}

// validateImageParams returns a message for obviously invalid parameters.
func validateImageParams(p *preset.ImageParams) string {
	if p.Kind != "" && p.Kind != "image" {
		return `kind must be "image"`
	}
	p.Kind = "image"
	if len(p.Outputs) == 0 || len(p.Outputs) > 4 {
		return "1–4 outputs required"
	}
	for _, o := range p.Outputs {
		if o.Format != "avif" && o.Format != "webp" {
			return "output format must be avif or webp"
		}
		if o.Name == "" {
			return "every output needs a name"
		}
	}
	if p.Resize.MaxWidth < 16 || p.Resize.MaxWidth > 8192 || p.Resize.MaxHeight < 16 || p.Resize.MaxHeight > 8192 {
		return "resize.maxWidth/maxHeight must be 16–8192"
	}
	switch p.Quality.Mode {
	case "budget":
		if p.Quality.MaxBytes < 1024 || p.Quality.MaxBytes > 10<<20 {
			return "quality.maxBytes must be 1 KB–10 MB"
		}
	case "ssim", "fixed":
	default:
		return "quality.mode must be budget, ssim or fixed"
	}
	return ""
}

// ───────────── preset versions ─────────────

// handleSavePreset: POST /admin/api/projects/{id}/presets/{slug}/versions
// { params } adds a new version (existing assets keep theirs); the preset is
// created when the slug is new (kind image). Body: { params, description? }.
func (s *Server) handleSavePresetVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")
	if !slugRe.MatchString(slug) {
		validationFailed(w, map[string]string{"slug": "lowercase letters, digits and dashes, starting with a letter (2–32)"})
		return
	}
	var in struct {
		Params      preset.ImageParams `json:"params"`
		Description string             `json:"description"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	if msg := validateImageParams(&in.Params); msg != "" {
		validationFailed(w, map[string]string{"params": msg})
		return
	}
	if in.Params.Limits.MaxInputBytes <= 0 {
		in.Params.Limits = preset.DefaultPresets0Limits()
	}
	raw, _ := json.Marshal(in.Params)
	existing, err := s.store.GetPresetBySlug(r.Context(), p.ID, slug)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	adminID := ""
	if a := adminFrom(r.Context()); a != nil {
		adminID = a.ID
	}
	var version int
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if existing == nil {
			np := &db.Preset{ID: db.NewID(db.PrefixPreset), ProjectID: p.ID, Slug: slug, Kind: "image",
				Description: strings.TrimSpace(in.Description), CurrentVersion: 1, Params: raw, CreatedAt: s.now()}
			version = 1
			if err := tx.CreatePreset(r.Context(), np, adminID); err != nil {
				return err
			}
			return s.audit(r, tx, "preset.create", &p.ID, &np.ID, map[string]any{"slug": slug})
		}
		if existing.Kind != "image" {
			return errNotImagePreset
		}
		var err error
		if version, err = tx.AddPresetVersion(r.Context(), existing.ID, raw, adminID, s.now()); err != nil {
			return err
		}
		return s.audit(r, tx, "preset.version", &p.ID, &existing.ID, map[string]any{"slug": slug, "version": version})
	})
	if errors.Is(err, errNotImagePreset) {
		validationFailed(w, map[string]string{"slug": "only image presets can be edited here"})
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cur, err := s.store.GetPresetBySlug(r.Context(), p.ID, slug)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	status := http.StatusOK
	if version == 1 {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, toPresetDTO(cur))
}

var errNotImagePreset = errors.New("not an image preset")
