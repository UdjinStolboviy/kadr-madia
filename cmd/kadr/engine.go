package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/engine/fakeimage"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// newImageEngine returns the libvips engine, the fake engine with
// --fake-engine, or an engine that reports a retryable failure when libvips
// is missing (the server still starts so the admin console can show the
// problem under System → Capabilities).
func newImageEngine(concurrency int, fake bool, log *slog.Logger) image.Engine {
	if fake {
		log.Warn("using the fake image engine: renditions are placeholders, not images (--fake-engine)")
		return fakeimage.New()
	}
	e, err := image.NewVipsCLI(image.VipsConfig{Nice: true, Timeout: 30 * time.Second, MaxConcurrency: concurrency})
	if err != nil {
		log.Error("image engine unavailable; image jobs will fail until libvips is installed", "err", err)
		return unavailableEngine{err: err}
	}
	return e
}

type unavailableEngine struct{ err error }

func (u unavailableEngine) fail() error {
	return &image.Error{Code: image.CodeEngineFailure, Detail: "libvips is not available", Retryable: true, Err: u.err}
}

func (u unavailableEngine) Probe(context.Context, string, preset.ImageLimits) (image.SourceInfo, error) {
	return image.SourceInfo{}, u.fail()
}

func (u unavailableEngine) Process(context.Context, image.SourceInfo, preset.ImageParams, string) (*image.Output, error) {
	return nil, u.fail()
}
