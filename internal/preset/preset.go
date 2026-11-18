// Package preset defines preset parameter schemas and the default presets
// created for every new project (docs/PIPELINES.md §1). It has no
// dependencies on engines or the pipeline, so both can import it.
package preset

import (
	"encoding/json"
	"slices"
)

// ImageParams is params_json for kind=image presets.
type ImageParams struct {
	Kind         string        `json:"kind"`
	Resize       Resize        `json:"resize"`
	Color        Color         `json:"color"`
	Sharpen      *Sharpen      `json:"sharpen"` // nil = disabled
	Outputs      []ImageOutput `json:"outputs"`
	Quality      Quality       `json:"quality"`
	Placeholder  Placeholder   `json:"placeholder"`
	Limits       ImageLimits   `json:"limits"`
	RetainOrigin bool          `json:"retainOriginal"`
}

// Resize describes geometry.
type Resize struct {
	MaxWidth  int     `json:"maxWidth"`
	MaxHeight int     `json:"maxHeight"`
	Fit       string  `json:"fit"`    // inside | cover
	Crop      *string `json:"crop"`   // attention | centre | null
	Aspect    *string `json:"aspect"` // e.g. "1:1" | null
}

// Color adjustments.
type Color struct {
	Grayscale bool `json:"grayscale"`
	Normalise bool `json:"normalise"`
}

// Sharpen parameters (vips sharpen).
type Sharpen struct {
	Sigma float64 `json:"sigma"`
	M1    float64 `json:"m1"`
	M2    float64 `json:"m2"`
}

// ImageOutput is one encoded rendition.
type ImageOutput struct {
	Name           string `json:"name"`
	Format         string `json:"format"` // avif | webp
	Effort         int    `json:"effort,omitempty"`
	Chroma         string `json:"chroma,omitempty"`
	SmartSubsample bool   `json:"smartSubsample,omitempty"`
}

// Quality selects the encoding strategy: budget | fixed | ssim.
type Quality struct {
	Mode          string  `json:"mode"`
	MaxBytes      int     `json:"maxBytes,omitempty"`
	QMax          int     `json:"qMax,omitempty"`
	QMin          int     `json:"qMin,omitempty"`
	DownscaleStep float64 `json:"downscaleStep,omitempty"`
	MinSide       int     `json:"minSide,omitempty"`
	Q             int     `json:"q,omitempty"`
	Target        float64 `json:"target,omitempty"`
}

// Placeholder controls ThumbHash / dominant color generation.
type Placeholder struct {
	ThumbHash     bool `json:"thumbhash"`
	DominantColor bool `json:"dominantColor"`
}

// ImageLimits guard against decompression bombs.
type ImageLimits struct {
	MaxInputBytes  int64    `json:"maxInputBytes"`
	MaxInputPixels int64    `json:"maxInputPixels"`
	Accept         []string `json:"accept"`
}

// VideoParams is params_json for kind=video presets.
type VideoParams struct {
	Kind         string      `json:"kind"`
	Video        VideoCodec  `json:"video"`
	Audio        AudioCodec  `json:"audio"`
	Container    Container   `json:"container"`
	Poster       Poster      `json:"poster"`
	Limits       VideoLimits `json:"limits"`
	RetainOrigin bool        `json:"retainOriginal"`
	// HLS additionally produces an adaptive HLS ladder (fMP4 segments).
	HLS bool `json:"hls,omitempty"`
}

// VideoCodec settings.
type VideoCodec struct {
	Codec    string `json:"codec"`
	MaxWidth int    `json:"maxWidth"`
	FPS      int    `json:"fps"`
	CRF      int    `json:"crf"`
	Maxrate  string `json:"maxrate"`
	Bufsize  string `json:"bufsize"`
	Preset   string `json:"preset"`
	HW       string `json:"hw"` // auto | vaapi | off
}

// AudioCodec settings.
type AudioCodec struct {
	Codec      string `json:"codec"`
	Bitrate    string `json:"bitrate"`
	Channels   int    `json:"channels"`
	SampleRate int    `json:"sampleRate"`
}

// Container settings.
type Container struct {
	Format    string `json:"format"`
	Faststart bool   `json:"faststart"`
}

// Poster frame settings.
type Poster struct {
	AtSec    float64 `json:"atSec"`
	Preset   string  `json:"preset"`
	MaxBytes int     `json:"maxBytes"`
	// Formats are the poster encodings to store (avif, webp). Empty means
	// both: AVIF plus a WebP fallback for clients without AVIF.
	Formats []string `json:"formats,omitempty"`
}

// DerivedImageFormats are the encodings allowed for posters and document
// thumbnails; DefaultDerivedFormats is used when a preset does not list any.
var (
	DerivedImageFormats   = []string{"avif", "webp"}
	DefaultDerivedFormats = []string{"avif", "webp"}
)

// DerivedOutputs returns the image outputs named name for the given
// formats (posters, document thumbnails); empty formats mean the default pair.
func DerivedOutputs(name string, formats []string) []ImageOutput {
	if len(formats) == 0 {
		formats = DefaultDerivedFormats
	}
	out := make([]ImageOutput, 0, len(formats))
	for _, f := range formats {
		switch f {
		case "avif":
			out = append(out, ImageOutput{Name: name, Format: "avif", Effort: 4, Chroma: "4:2:0"})
		case "webp":
			out = append(out, ImageOutput{Name: name, Format: "webp", SmartSubsample: true})
		}
	}
	return out
}

// ValidDerivedFormats reports whether formats is empty (default) or a list
// of distinct supported encodings.
func ValidDerivedFormats(formats []string) bool {
	seen := map[string]bool{}
	for _, f := range formats {
		if seen[f] || !slices.Contains(DerivedImageFormats, f) {
			return false
		}
		seen[f] = true
	}
	return true
}

// VideoLimits guard input size and duration.
type VideoLimits struct {
	MaxInputBytes  int64 `json:"maxInputBytes"`
	MaxDurationSec int   `json:"maxDurationSec"`
	MaxWidth       int   `json:"maxWidth"`
	MaxHeight      int   `json:"maxHeight"`
}

// DefaultPreset is a preset seeded into every new project as version 1.
type DefaultPreset struct {
	Slug        string
	Kind        string
	Description string
	Params      json.RawMessage
}

func ptr[T any](v T) *T { return &v }

var (
	defaultImageLimits = ImageLimits{MaxInputBytes: 25 << 20, MaxInputPixels: 40_000_000, Accept: []string{"jpeg", "png", "webp", "heic", "avif", "gif"}}
	defaultOutputs     = []ImageOutput{
		{Name: "main", Format: "avif", Effort: 4, Chroma: "4:2:0"},
		{Name: "main", Format: "webp", SmartSubsample: true},
	}
	defaultVideoLimits = VideoLimits{MaxInputBytes: 2 << 30, MaxDurationSec: 300, MaxWidth: 4096, MaxHeight: 4096}
	defaultAudio       = AudioCodec{Codec: "aac", Bitrate: "64k", Channels: 1, SampleRate: 44100}
	defaultContainer   = Container{Format: "mp4", Faststart: true}
)

// DefaultPresets returns the standard presets from docs/PIPELINES.md §1.
func DefaultPresets() []DefaultPreset {
	items := []struct {
		slug, kind, desc string
		params           any
	}{
		{"photo-lite", "image", "Cards, inventory, thumbnails: max side 800, ≤ 20 KB, AVIF + WebP", ImageParams{
			Kind:        "image",
			Resize:      Resize{MaxWidth: 800, MaxHeight: 800, Fit: "inside"},
			Sharpen:     &Sharpen{Sigma: 0.6, M1: 0.5, M2: 1.5},
			Outputs:     defaultOutputs,
			Quality:     Quality{Mode: "budget", MaxBytes: 20480, QMax: 60, QMin: 30, DownscaleStep: 0.85, MinSide: 320},
			Placeholder: Placeholder{ThumbHash: true, DominantColor: true},
			Limits:      defaultImageLimits,
		}},
		{"photo-balanced", "image", "Full-screen viewing: max side 1440, SSIM target, AVIF + WebP", ImageParams{
			Kind:        "image",
			Resize:      Resize{MaxWidth: 1440, MaxHeight: 1440, Fit: "inside"},
			Outputs:     defaultOutputs,
			Quality:     Quality{Mode: "ssim", Target: 0.985},
			Placeholder: Placeholder{ThumbHash: true, DominantColor: true},
			Limits:      defaultImageLimits,
		}},
		{"doc-lite", "image", "Receipts, contracts, labels: grayscale + normalise, max side 1100, ≤ 20 KB", ImageParams{
			Kind:        "image",
			Resize:      Resize{MaxWidth: 1100, MaxHeight: 1100, Fit: "inside"},
			Color:       Color{Grayscale: true, Normalise: true},
			Sharpen:     &Sharpen{Sigma: 0.85, M1: 0.5, M2: 1.5},
			Outputs:     defaultOutputs,
			Quality:     Quality{Mode: "budget", MaxBytes: 20480, QMax: 60, QMin: 30, DownscaleStep: 0.85, MinSide: 640},
			Placeholder: Placeholder{ThumbHash: false, DominantColor: false},
			Limits:      defaultImageLimits,
		}},
		{"avatar", "image", "Avatars: smart crop 1:1 (attention), 256 px, ≤ 8 KB", ImageParams{
			Kind:        "image",
			Resize:      Resize{MaxWidth: 256, MaxHeight: 256, Fit: "cover", Crop: ptr("attention"), Aspect: ptr("1:1")},
			Sharpen:     &Sharpen{Sigma: 0.5, M1: 0.5, M2: 1.5},
			Outputs:     defaultOutputs,
			Quality:     Quality{Mode: "budget", MaxBytes: 8192, QMax: 60, QMin: 30, DownscaleStep: 0.85, MinSide: 128},
			Placeholder: Placeholder{ThumbHash: true, DominantColor: true},
			Limits:      defaultImageLimits,
		}},
		{"video-720p", "video", "Regular clips: H.264 CRF 26, maxrate 900k, 30 fps, AAC 64k mono, poster ≤ 15 KB", VideoParams{
			Kind:      "video",
			Video:     VideoCodec{Codec: "h264", MaxWidth: 1280, FPS: 30, CRF: 26, Maxrate: "900k", Bufsize: "1800k", Preset: "slow", HW: "auto"},
			Audio:     defaultAudio,
			Container: defaultContainer,
			Poster:    Poster{AtSec: 1.0, Preset: "photo-lite", MaxBytes: 15360},
			Limits:    defaultVideoLimits,
		}},
		{"video-480p-eco", "video", "Economy: H.264 CRF 28, maxrate 500k, 854 px", VideoParams{
			Kind:      "video",
			Video:     VideoCodec{Codec: "h264", MaxWidth: 854, FPS: 30, CRF: 28, Maxrate: "500k", Bufsize: "1000k", Preset: "slow", HW: "auto"},
			Audio:     AudioCodec{Codec: "aac", Bitrate: "48k", Channels: 1, SampleRate: 44100},
			Container: defaultContainer,
			Poster:    Poster{AtSec: 1.0, Preset: "photo-lite", MaxBytes: 15360},
			Limits:    defaultVideoLimits,
		}},
	}
	items = append(items, modePresets()...)
	items = append(items, filePresets()...)
	out := make([]DefaultPreset, 0, len(items))
	for _, it := range items {
		b, err := json.Marshal(it.params)
		if err != nil {
			panic(err) // static data
		}
		out = append(out, DefaultPreset{Slug: it.slug, Kind: it.kind, Description: it.desc, Params: b})
	}
	return out
}

// DefaultPresets0Limits returns the limits applied to image presets saved
// without explicit limits.
func DefaultPresets0Limits() ImageLimits { return defaultImageLimits }
