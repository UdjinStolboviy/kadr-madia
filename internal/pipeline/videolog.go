package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/engine/video"
)

// recordTranscode makes the encoder choice explicit: one structured log line
// per transcode (WARN when VAAPI silently fell back to libx264) and the
// metrics kadr_video_transcodes_total{encoder} and
// kadr_video_hw_fallbacks_total, plus seconds of video per encoder for speed.
func (s *Service) recordTranscode(pc *processCtx, info video.SourceInfo, vo *video.Output, elapsed time.Duration) {
	requested := pc.vparams.Video.HW
	if requested == "" {
		requested = "auto"
	}
	speed := 0.0 // seconds of video per wall-clock second (×realtime)
	if elapsed > 0 && vo.DurationMs > 0 {
		speed = float64(vo.DurationMs) / float64(elapsed.Milliseconds()+1)
	}
	attrs := []any{
		"asset_id", pc.asset.ID, "project_id", pc.project.ID, "job_id", pc.job.ID,
		"encoder", vo.Encoder, "hw_requested", requested, "hw_fallback", vo.HWFallback,
		"src_codec", info.VideoCodec, "src_width", info.Width, "src_height", info.Height,
		"duration_ms", vo.DurationMs, "out_bytes", vo.Bytes, "out_width", vo.Width, "out_height", vo.Height,
		"elapsed_ms", elapsed.Milliseconds(), "speed_x", fmt.Sprintf("%.2f", speed), "cpu_ms", vo.CPUMillis,
	}
	level := slog.LevelInfo
	if vo.HWFallback {
		level = slog.LevelWarn
		attrs = append(attrs, "hw_error", vo.HWError)
	}
	s.log.Log(context.Background(), level, "video transcoded", attrs...)
	s.metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_video_transcodes_total{encoder=%q}`, vo.Encoder)).Inc()
	s.metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_video_transcode_media_seconds_total{encoder=%q}`, vo.Encoder)).Add(int(vo.DurationMs / 1000))
	s.metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_video_transcode_wall_seconds_total{encoder=%q}`, vo.Encoder)).Add(int(elapsed.Seconds()))
	if vo.HWFallback {
		s.metrics.GetOrCreateCounter("kadr_video_hw_fallbacks_total").Inc()
	}
}
