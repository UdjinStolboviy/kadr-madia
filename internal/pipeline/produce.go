package pipeline

import (
	"context"
	"os"
	"path/filepath"

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
}

type producedFile struct {
	name, format, path string
	bytes              int64
	width, height      int
	quality            int
	bitrate            int64
	overBudget         bool
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
	vo, err := s.video.Transcode(ctx, info, pc.vparams, outDir, func(pct int) {
		s.log.Debug("transcode progress", "job_id", pc.job.ID, "pct", pct)
	})
	if err != nil {
		return nil, classifyEngineError(err)
	}
	p := &produced{cpuMillis: vo.CPUMillis, durationMs: vo.DurationMs, srcWidth: info.Width, srcHeight: info.Height,
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
