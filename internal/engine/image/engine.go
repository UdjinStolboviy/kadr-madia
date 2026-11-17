// Package image defines the image processing engine used by the pipeline.
//
// The pipeline downloads the source into a per-job working directory, calls
// Probe to validate it, then Process to produce encoded renditions on local
// disk. The pipeline (not the engine) uploads the files to storage, records
// Blob/Rendition rows and cleans up the working directory.
//
// The default implementation (VipsCLI) shells out to the libvips command line
// tools; see docs/adr/0007-libvips-cli-image-engine.md and
// docs/adr/0009-byte-budget-encoding.md.
package image

import (
	"context"
	"errors"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Engine processes still images.
type Engine interface {
	// Probe inspects the file at path without fully decoding it: detects the
	// real format from magic bytes and reads dimensions. It enforces
	// limits.MaxInputBytes, limits.MaxInputPixels and limits.Accept and
	// returns a *Error with the matching Code when a limit is violated.
	Probe(ctx context.Context, path string, limits preset.ImageLimits) (SourceInfo, error)

	// Process runs the preset on the source and writes every output of
	// params.Outputs into workDir. It returns one Result per output in the
	// same order as params.Outputs, plus placeholder data when requested.
	// Process must not modify or delete the source file.
	Process(ctx context.Context, src SourceInfo, params preset.ImageParams, workDir string) (*Output, error)
}

// SourceInfo describes a validated source file.
type SourceInfo struct {
	Path        string // absolute path to the source on local disk
	Format      string // jpeg | png | webp | heic | avif | gif | tiff
	ContentType string // detected MIME type, e.g. "image/heic"
	Bytes       int64
	Width       int // after EXIF orientation is applied
	Height      int
	Pages       int // > 1 for animated GIF/WebP; the engine uses the first page
}

// Output is the result of Process.
type Output struct {
	Renditions    []Rendition
	ThumbHash     []byte // nil unless params.Placeholder.ThumbHash
	DominantColor string // "#rrggbb", empty unless params.Placeholder.DominantColor
	CPUMillis     int64  // total CPU time of child processes, for usage metering
}

// Rendition is one encoded file written to the work directory.
type Rendition struct {
	Name       string // from preset output, e.g. "main"
	Format     string // avif | webp
	Path       string // absolute path inside workDir
	Bytes      int64
	Width      int
	Height     int
	Quality    int  // chosen encoder quality
	Iterations int  // encodes performed by the budget search
	OverBudget bool // true if quality.maxBytes could not be met at minSide/qMin
}

// Error codes surfaced to API clients as asset.failed error.code.
const (
	CodeUnsupportedMedia = "unsupported_media"
	CodeTooLarge         = "too_large"       // bytes over limits.MaxInputBytes
	CodeTooManyPixels    = "too_many_pixels" // width*height over limits.MaxInputPixels
	CodeCorrupt          = "corrupt"
	CodeTimeout          = "timeout"
	CodeEngineFailure    = "engine_failure" // vips missing/crashed; retryable
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
