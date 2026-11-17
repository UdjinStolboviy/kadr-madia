// Package fakeimage is a deterministic image.Engine for tests and for
// `kadr serve --fake-engine` on machines without libvips. It does not decode
// images: it writes small placeholder files whose content depends only on
// the source bytes and the output definition.
//
// Behaviour can be steered by the source content (useful in end-to-end
// tests): a file starting with "CORRUPT" fails Probe with a non-retryable
// "corrupt" error, one starting with "FLAKY" fails Process with a retryable
// "engine_failure".
package fakeimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Source dimensions reported by Probe.
const (
	Width  = 1600
	Height = 1200
)

// Engine implements image.Engine.
type Engine struct {
	ProbeCalls   atomic.Int64
	ProcessCalls atomic.Int64
}

var _ image.Engine = (*Engine)(nil)

// New returns a fake engine.
func New() *Engine { return &Engine{} }

// Probe validates size and the steering prefixes and reports fixed dimensions.
func (e *Engine) Probe(ctx context.Context, path string, limits preset.ImageLimits) (image.SourceInfo, error) {
	e.ProbeCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return image.SourceInfo{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return image.SourceInfo{}, &image.Error{Code: image.CodeEngineFailure, Detail: "stat source", Retryable: true, Err: err}
	}
	if limits.MaxInputBytes > 0 && st.Size() > limits.MaxInputBytes {
		return image.SourceInfo{}, &image.Error{Code: image.CodeTooLarge, Detail: fmt.Sprintf("%d bytes over the limit of %d", st.Size(), limits.MaxInputBytes)}
	}
	head := make([]byte, 16)
	f, err := os.Open(path)
	if err != nil {
		return image.SourceInfo{}, &image.Error{Code: image.CodeEngineFailure, Detail: "open source", Retryable: true, Err: err}
	}
	n, _ := f.Read(head)
	f.Close()
	head = head[:n]
	if bytes.HasPrefix(head, []byte("CORRUPT")) {
		return image.SourceInfo{}, &image.Error{Code: image.CodeCorrupt, Detail: "fake engine: corrupt source"}
	}
	format, ctype := "jpeg", "image/jpeg"
	if bytes.HasPrefix(head, []byte("\x89PNG")) {
		format, ctype = "png", "image/png"
	}
	return image.SourceInfo{Path: path, Format: format, ContentType: ctype, Bytes: st.Size(), Width: Width, Height: Height, Pages: 1}, nil
}

// Process writes one small deterministic file per output.
func (e *Engine) Process(ctx context.Context, src image.SourceInfo, params preset.ImageParams, workDir string) (*image.Output, error) {
	e.ProcessCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, &image.Error{Code: image.CodeEngineFailure, Detail: "read source", Retryable: true, Err: err}
	}
	if bytes.HasPrefix(data, []byte("FLAKY")) {
		return nil, &image.Error{Code: image.CodeEngineFailure, Detail: "fake engine: transient failure", Retryable: true}
	}
	sum := sha256.Sum256(data)
	w, h := fit(src.Width, src.Height, params.Resize.MaxWidth, params.Resize.MaxHeight)
	out := &image.Output{CPUMillis: 7}
	for i, o := range params.Outputs {
		body := fmt.Sprintf("fake %s %s %dx%d %x\n", o.Name, o.Format, w, h, sum[:8])
		body += string(bytes.Repeat([]byte{'.'}, 64*(i+1)))
		path := filepath.Join(workDir, fmt.Sprintf("%s.%s", o.Name, o.Format))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return nil, &image.Error{Code: image.CodeEngineFailure, Detail: "write output", Retryable: true, Err: err}
		}
		out.Renditions = append(out.Renditions, image.Rendition{Name: o.Name, Format: o.Format, Path: path,
			Bytes: int64(len(body)), Width: w, Height: h, Quality: 50, Iterations: 1})
	}
	if params.Placeholder.ThumbHash {
		out.ThumbHash = append([]byte(nil), sum[:16]...)
	}
	if params.Placeholder.DominantColor {
		out.DominantColor = fmt.Sprintf("#%02x%02x%02x", sum[0], sum[1], sum[2])
	}
	return out, nil
}

// fit scales w×h to fit inside maxW×maxH (0 = unbounded) without upscaling.
func fit(w, h, maxW, maxH int) (int, int) {
	scale := 1.0
	if maxW > 0 && w > maxW {
		scale = min(scale, float64(maxW)/float64(w))
	}
	if maxH > 0 && h > maxH {
		scale = min(scale, float64(maxH)/float64(h))
	}
	return max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
}
