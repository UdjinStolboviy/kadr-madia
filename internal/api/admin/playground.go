package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// handleSavePresetVersion: POST /admin/api/projects/{id}/presets/{slug}/versions
// { params, description? } adds a new version (existing assets keep theirs);
// the preset is created when the slug is new (the kind comes from params.kind,
// default image). Works for image and video presets, including the
// compression-mode presets.
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
		Params      json.RawMessage `json:"params"`
		Description string          `json:"description"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	existing, err := s.store.GetPresetBySlug(r.Context(), p.ID, slug)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	kind := "image"
	if existing != nil {
		kind = existing.Kind
	} else {
		var k struct{ Kind string }
		_ = json.Unmarshal(in.Params, &k)
		if k.Kind == "video" || k.Kind == "file" {
			kind = k.Kind
		}
	}
	raw, msg := normalizePresetParams(kind, in.Params)
	if msg != "" {
		validationFailed(w, map[string]string{"params": msg})
		return
	}
	s.savePresetVersion(w, r, p, existing, slug, kind, strings.TrimSpace(in.Description), raw)
}

// savePresetVersion creates the preset or appends a version, and replies with the preset.
func (s *Server) savePresetVersion(w http.ResponseWriter, r *http.Request, p *db.Project, existing *db.Preset, slug, kind, desc string, raw json.RawMessage) {
	adminID := actorID(r.Context())
	var version int
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if existing == nil {
			np := &db.Preset{ID: db.NewID(db.PrefixPreset), ProjectID: p.ID, Slug: slug, Kind: kind,
				Description: desc, CurrentVersion: 1, Params: raw, CreatedAt: s.now()}
			version = 1
			if err := tx.CreatePreset(r.Context(), np, adminID); err != nil {
				return err
			}
			return s.audit(r, tx, "preset.create", &p.ID, &np.ID, map[string]any{"slug": slug})
		}
		var err error
		if version, err = tx.AddPresetVersion(r.Context(), existing.ID, raw, adminID, s.now()); err != nil {
			return err
		}
		return s.audit(r, tx, "preset.version", &p.ID, &existing.ID, map[string]any{"slug": slug, "version": version})
	})
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

// normalizePresetParams validates params for kind and returns canonical JSON.
func normalizePresetParams(kind string, raw json.RawMessage) (json.RawMessage, string) {
	if len(raw) == 0 {
		return nil, "required"
	}
	if kind == "video" {
		var v preset.VideoParams
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, "must be valid video preset parameters JSON"
		}
		if msg := validateVideoParams(&v); msg != "" {
			return nil, msg
		}
		b, _ := json.Marshal(v)
		return b, ""
	}
	if kind == "file" {
		var f preset.FileParams
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, "must be valid document preset parameters JSON"
		}
		if msg := validateFileParams(&f); msg != "" {
			return nil, msg
		}
		b, _ := json.Marshal(f)
		return b, ""
	}
	var ip preset.ImageParams
	if err := json.Unmarshal(raw, &ip); err != nil {
		return nil, "must be valid image preset parameters JSON"
	}
	if msg := validateImageParams(&ip); msg != "" {
		return nil, msg
	}
	if ip.Limits.MaxInputBytes <= 0 {
		ip.Limits = preset.DefaultPresets0Limits()
	}
	b, _ := json.Marshal(ip)
	return b, ""
}

var bitrateRe = regexp.MustCompile(`^[1-9][0-9]{1,5}[kKmM]$`)

// validateVideoParams returns a message for obviously invalid parameters.
func validateVideoParams(v *preset.VideoParams) string {
	if v.Kind != "" && v.Kind != "video" {
		return `kind must be "video"`
	}
	v.Kind = "video"
	if v.Video.Codec == "" {
		v.Video.Codec = "h264"
	}
	if v.Video.Codec != "h264" && v.Video.Codec != "av1" {
		return `video.codec must be "h264" or "av1"`
	}
	if v.Video.Codec == "av1" && v.HLS {
		return "HLS is produced with H.264 only; use codec h264 with hls"
	}
	if v.Video.MaxWidth < 160 || v.Video.MaxWidth > 4096 {
		return "video.maxWidth must be 160–4096"
	}
	if v.Video.FPS < 1 || v.Video.FPS > 60 {
		return "video.fps must be 1–60"
	}
	if maxCRF := map[string]int{"h264": 40, "av1": 63}[v.Video.Codec]; v.Video.CRF < 14 || v.Video.CRF > maxCRF {
		return fmt.Sprintf("video.crf must be 14–%d for %s", maxCRF, v.Video.Codec)
	}
	if v.Video.Maxrate != "" && !bitrateRe.MatchString(v.Video.Maxrate) {
		return `video.maxrate must look like "900k" or "4M"`
	}
	if v.Video.Bufsize != "" && !bitrateRe.MatchString(v.Video.Bufsize) {
		return `video.bufsize must look like "1800k" or "8M"`
	}
	switch v.Video.Preset {
	case "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower":
	case "":
		v.Video.Preset = "slow"
	default:
		return "video.preset is not an x264 preset"
	}
	switch v.Video.HW {
	case "auto", "vaapi", "off":
	case "":
		v.Video.HW = "auto"
	default:
		return "video.hw must be auto, vaapi or off"
	}
	if v.Audio.Bitrate != "" && !bitrateRe.MatchString(v.Audio.Bitrate) {
		return `audio.bitrate must look like "96k"`
	}
	if v.Audio.Channels < 0 || v.Audio.Channels > 2 {
		return "audio.channels must be 1 or 2"
	}
	if v.Container.Format == "" {
		v.Container.Format = "mp4"
	}
	v.Container.Faststart = true
	if v.Poster.MaxBytes <= 0 {
		v.Poster.MaxBytes = 15360
	}
	if v.Limits.MaxInputBytes <= 0 {
		v.Limits = preset.VideoLimits{MaxInputBytes: 2 << 30, MaxDurationSec: 300, MaxWidth: 4096, MaxHeight: 4096}
	}
	return ""
}

// validateFileParams returns a message for obviously invalid document preset parameters.
func validateFileParams(f *preset.FileParams) string {
	if f.Kind != "" && f.Kind != "file" {
		return `kind must be "file"`
	}
	f.Kind = "file"
	if f.Limits.MaxInputBytes <= 0 {
		f.Limits.MaxInputBytes = 100 << 20
	}
	if f.Limits.MaxInputBytes > 5<<30 {
		return "limits.maxInputBytes must be at most 5 GB"
	}
	if len(f.Limits.Accept) == 0 {
		f.Limits.Accept = preset.DefaultFileFormats
	}
	for _, a := range f.Limits.Accept {
		if !slices.Contains(preset.DefaultFileFormats, a) {
			return "limits.accept: unsupported format " + a
		}
	}
	switch f.Optimize.Strategy {
	case "", preset.FileStrategyNone:
		f.Optimize.Strategy = preset.FileStrategyNone
	case preset.FileStrategyLossless:
	case preset.FileStrategyDownsample:
		if f.Optimize.ImageDPI < 36 || f.Optimize.ImageDPI > 600 {
			return "optimize.imageDpi must be 36–600"
		}
		if f.Optimize.JPEGQuality < 10 || f.Optimize.JPEGQuality > 100 {
			return "optimize.jpegQuality must be 10–100"
		}
	default:
		return "optimize.strategy must be none, lossless or downsample"
	}
	if f.Optimize.MinSavingsPercent < 0 || f.Optimize.MinSavingsPercent > 90 {
		return "optimize.minSavingsPercent must be 0–90"
	}
	if f.Thumbnail.Enabled {
		if f.Thumbnail.MaxSide < 96 || f.Thumbnail.MaxSide > 2048 {
			return "thumbnail.maxSide must be 96–2048"
		}
		if f.Thumbnail.MaxBytes < 1024 || f.Thumbnail.MaxBytes > 1<<20 {
			return "thumbnail.maxBytes must be 1 KB–1 MB"
		}
	}
	return ""
}
