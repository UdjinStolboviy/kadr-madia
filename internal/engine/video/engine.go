// Package video defines the video processing engine and its ffmpeg
// implementation (docs/PIPELINES.md §3).
//
// Like the image engine, it works on local files only: the pipeline downloads
// the source, calls Probe to validate it, then Transcode to produce the MP4
// and a poster frame; the pipeline uploads the results and cleans up.
package video

import (
	"context"
	"errors"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Engine processes videos.
type Engine interface {
	// Probe validates the file at path with ffprobe and enforces limits.
	Probe(ctx context.Context, path string, limits preset.VideoLimits) (SourceInfo, error)

	// Transcode writes the encoded video (<workDir>/video.mp4) and a poster
	// frame (<workDir>/poster.png, still to be compressed by the image
	// engine). progress, if non-nil, receives values in [0,100].
	Transcode(ctx context.Context, src SourceInfo, params preset.VideoParams, workDir string, progress func(pct int)) (*Output, error)
}

// SourceInfo describes a validated source video.
type SourceInfo struct {
	Path        string
	Bytes       int64
	Container   string // ffprobe format_name
	VideoCodec  string
	AudioCodec  string // empty when there is no audio
	Width       int    // display size, rotation applied
	Height      int
	DurationSec float64
	FPS         float64
	Rotation    int // degrees from display matrix, informational
}

// Output is the result of Transcode.
type Output struct {
	VideoPath  string
	PosterPath string
	Bytes      int64
	Width      int
	Height     int
	DurationMs int64
	Encoder    string // libx264 | h264_vaapi
	HasAudio   bool
	CPUMillis  int64
}

// Error codes surfaced as asset.failed error.code.
const (
	CodeUnsupportedMedia = "unsupported_media"
	CodeTooLarge         = "too_large"
	CodeTooLong          = "too_long"
	CodeTooManyPixels    = "too_many_pixels"
	CodeCorrupt          = "corrupt"
	CodeTimeout          = "timeout"
	CodeEngineFailure    = "engine_failure"
)

// Error is a classified engine error.
type Error struct {
	Code      string
	Detail    string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Detail + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Detail
}

func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a *Error from err.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
