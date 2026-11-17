package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/engine/file"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/video"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// produced is the engine-independent result of processing a source: the
// files to upload plus facts about the source for the blob row.
type produced struct {
	files          []producedFile
	thumbHash      []byte
	dominantColor  string
	cpuMillis      int64
	durationMs     int64
	srcWidth       int
	srcHeight      int
	srcFormat      string
	srcContentType string
	retainOrigin   bool
	hls            *video.HLSOutput // optional HLS package (video presets with hls=true)
}

type producedFile struct {
	name, format, path string
	bytes              int64
	width, height      int
	quality            int
	bitrate            int64
	overBudget         bool
	disposition        string // Content-Disposition stored with the object
}

func (s *Service) produceImage(ctx context.Context, pc *processCtx, srcPath, outDir string) (*produced, error) {
	info, err := s.engine.Probe(ctx, srcPath, pc.params.Limits)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	out, err := s.engine.Process(ctx, info, pc.params, outDir)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	if len(out.Renditions) == 0 {
		return nil, permanent(image.CodeEngineFailure, "engine produced no renditions", nil)
	}
	p := &produced{thumbHash: out.ThumbHash, dominantColor: out.DominantColor, cpuMillis: out.CPUMillis,
		srcWidth: info.Width, srcHeight: info.Height, srcFormat: info.Format, srcContentType: info.ContentType,
		retainOrigin: pc.params.RetainOrigin}
	for _, r := range out.Renditions {
		p.files = append(p.files, producedFile{name: r.Name, format: r.Format, path: r.Path, bytes: r.Bytes,
			width: r.Width, height: r.Height, quality: r.Quality, overBudget: r.OverBudget})
	}
	return p, nil
}

// posterParams builds the image preset used to compress the poster frame:
// the budget search with the video preset's poster byte limit.
func posterParams(v preset.VideoParams) preset.ImageParams {
	max := v.Poster.MaxBytes
	if max <= 0 {
		max = 15 << 10
	}
	return preset.ImageParams{
		Kind:   "image",
		Resize: preset.Resize{MaxWidth: 640, MaxHeight: 640, Fit: "inside"},
		Outputs: []preset.ImageOutput{
			{Name: "poster", Format: "avif", Effort: 4, Chroma: "4:2:0"},
			{Name: "poster", Format: "webp", SmartSubsample: true},
		},
		Quality:     preset.Quality{Mode: "budget", MaxBytes: max, QMax: 60, QMin: 25, DownscaleStep: 0.85, MinSide: 240},
		Placeholder: preset.Placeholder{ThumbHash: true, DominantColor: true},
		Limits:      preset.ImageLimits{MaxInputBytes: 64 << 20, MaxInputPixels: 40_000_000, Accept: []string{"png"}},
	}
}

func (s *Service) produceVideo(ctx context.Context, pc *processCtx, srcPath, outDir string) (*produced, error) {
	if s.video == nil {
		return nil, &jobError{code: video.CodeEngineFailure, detail: "video engine is not configured"}
	}
	info, err := s.video.Probe(ctx, srcPath, pc.vparams.Limits)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	// Progress is 0-100 overall; with an HLS ladder the MP4 takes the first
	// half and the ladder the second.
	report := func(pct int) {
		if pc.job.WorkerID != nil {
			if err := s.store.SetJobProgress(ctx, pc.job.ID, *pc.job.WorkerID, pct); err != nil {
				s.log.Debug("record job progress", "job_id", pc.job.ID, "err", err)
			}
		}
	}
	mp4Report := report
	if pc.vparams.HLS {
		mp4Report = func(pct int) { report(pct / 2) }
	}
	vo, err := s.video.Transcode(ctx, info, pc.vparams, outDir, mp4Report)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	var ho *video.HLSOutput
	if pc.vparams.HLS {
		if ho, err = s.packageHLS(ctx, pc, info, outDir, func(pct int) { report(50 + pct/2) }); err != nil {
			return nil, err
		}
	}
	p := &produced{hls: ho, cpuMillis: vo.CPUMillis, durationMs: vo.DurationMs, srcWidth: info.Width, srcHeight: info.Height,
		srcFormat: videoExt(info.Container), srcContentType: videoMIME(info.Container), retainOrigin: pc.vparams.RetainOrigin}
	var bitrate int64
	if vo.DurationMs > 0 {
		bitrate = vo.Bytes * 8 * 1000 / vo.DurationMs
	}
	p.files = append(p.files, producedFile{name: "main", format: "mp4", path: vo.VideoPath, bytes: vo.Bytes,
		width: vo.Width, height: vo.Height, bitrate: bitrate})

	// Poster: compress the extracted frame with the image engine.
	posterDir := filepath.Join(outDir, "poster")
	if err := os.MkdirAll(posterDir, 0o750); err != nil {
		return nil, err
	}
	pp := posterParams(pc.vparams)
	pinfo, err := s.engine.Probe(ctx, vo.PosterPath, pp.Limits)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	po, err := s.engine.Process(ctx, pinfo, pp, posterDir)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	p.thumbHash, p.dominantColor, p.cpuMillis = po.ThumbHash, po.DominantColor, p.cpuMillis+po.CPUMillis
	for _, r := range po.Renditions {
		p.files = append(p.files, producedFile{name: r.Name, format: r.Format, path: r.Path, bytes: r.Bytes,
			width: r.Width, height: r.Height, quality: r.Quality, overBudget: r.OverBudget})
	}
	return p, nil
}

func videoExt(container string) string {
	switch {
	case contains(container, "matroska"), contains(container, "webm"):
		return "mkv"
	case contains(container, "avi"):
		return "avi"
	case contains(container, "mpeg"):
		return "mpg"
	case contains(container, "mov"), contains(container, "mp4"):
		return "mp4"
	}
	return "bin"
}

func videoMIME(container string) string {
	switch videoExt(container) {
	case "mkv":
		if contains(container, "webm") {
			return "video/webm"
		}
		return "video/x-matroska"
	case "avi":
		return "video/x-msvideo"
	case "mpg":
		return "video/mpeg"
	case "mp4":
		if contains(container, "mov") && !contains(container, "mp4") {
			return "video/quicktime"
		}
		return "video/mp4"
	}
	return "application/octet-stream"
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ───────────── documents ─────────────

func thumbParams(f preset.FileParams) preset.ImageParams {
	side := f.Thumbnail.MaxSide
	if side <= 0 {
		side = 640
	}
	max := f.Thumbnail.MaxBytes
	if max <= 0 {
		max = 20 << 10
	}
	return preset.ImageParams{
		Kind:   "image",
		Resize: preset.Resize{MaxWidth: side, MaxHeight: side, Fit: "inside"},
		Outputs: []preset.ImageOutput{
			{Name: "thumb", Format: "avif", Effort: 4, Chroma: "4:2:0"},
			{Name: "thumb", Format: "webp", SmartSubsample: true},
		},
		Quality:     preset.Quality{Mode: "budget", MaxBytes: max, QMax: 60, QMin: 25, DownscaleStep: 0.85, MinSide: 200},
		Placeholder: preset.Placeholder{ThumbHash: true, DominantColor: true},
		Limits:      preset.ImageLimits{MaxInputBytes: 64 << 20, MaxInputPixels: 40_000_000, Accept: []string{"png"}},
	}
}

func (s *Service) produceFile(ctx context.Context, pc *processCtx, srcPath, outDir string) (*produced, error) {
	if s.file == nil {
		return nil, &jobError{code: file.CodeEngineFailure, detail: "the document engine is not configured"}
	}
	name := deref(pc.upload.Filename)
	info, err := s.file.Probe(ctx, srcPath, name, pc.fparams.Limits)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	fo, err := s.file.Process(ctx, info, pc.fparams, outDir)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	if fo.Main.Skipped != "" && fo.Main.Skipped != "not_pdf" && fo.Main.Skipped != "disabled" {
		s.log.Info("document stored without optimisation", "job_id", pc.job.ID, "reason", fo.Main.Skipped, "bytes", fo.Main.OriginalBytes)
	}
	p := &produced{cpuMillis: fo.CPUMillis, srcFormat: info.Format, srcContentType: info.ContentType,
		retainOrigin: pc.fparams.RetainOrigin && fo.Main.Optimized}
	p.files = append(p.files, producedFile{name: "main", format: fo.Main.Format, path: fo.Main.Path, bytes: fo.Main.Bytes,
		disposition: contentDispositionFor(name, fo.Main.Format)})
	if fo.ThumbPath != "" {
		tdir := filepath.Join(outDir, "thumb")
		if err := os.MkdirAll(tdir, 0o750); err != nil {
			return nil, err
		}
		tp := thumbParams(pc.fparams)
		tinfo, err := s.engine.Probe(ctx, fo.ThumbPath, tp.Limits)
		if err == nil {
			var to *image.Output
			if to, err = s.engine.Process(ctx, tinfo, tp, tdir); err == nil {
				p.thumbHash, p.dominantColor, p.cpuMillis = to.ThumbHash, to.DominantColor, p.cpuMillis+to.CPUMillis
				for _, r := range to.Renditions {
					p.files = append(p.files, producedFile{name: r.Name, format: r.Format, path: r.Path, bytes: r.Bytes,
						width: r.Width, height: r.Height, quality: r.Quality, overBudget: r.OverBudget})
				}
			}
		}
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		} // a thumbnail that cannot be compressed is skipped; the document itself is stored
	}
	return p, nil
}

// contentDispositionFor forces a download with a safe file name, so a stored
// document can never be rendered inline from the CDN origin.
func contentDispositionFor(name, format string) string {
	base := strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if base == "" || base == "." || base == "/" {
		base = "document." + format
	}
	var ascii strings.Builder
	for _, r := range base {
		switch {
		case r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == ';':
			ascii.WriteRune('_')
		case r < 0x7f:
			ascii.WriteRune(r)
		default:
			ascii.WriteRune('_')
		}
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii.String(), url.PathEscape(base))
}
