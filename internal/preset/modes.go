package preset

// Compression modes: three ready-made quality/size trade-offs for images and
// videos, available in every project as the presets image-<mode> and
// video-<mode>. Projects may edit them (new preset versions); "reset" restores
// the defaults below.
//
//	compact  — good quality at a very small size
//	balanced — the golden middle (default)
//	quality  — ideal quality at the smallest size that still achieves it
const (
	ModeCompact  = "compact"
	ModeBalanced = "balanced"
	ModeQuality  = "quality"

	// DefaultMode is used when neither a preset nor a mode is requested.
	DefaultMode = ModeBalanced
)

// Modes lists the modes from smallest to best quality.
var Modes = []string{ModeCompact, ModeBalanced, ModeQuality}

// ValidMode reports whether m is a known mode.
func ValidMode(m string) bool {
	for _, x := range Modes {
		if x == m {
			return true
		}
	}
	return false
}

// ModeSlug returns the preset slug of a mode for kind "image" or "video".
func ModeSlug(kind, mode string) string { return kind + "-" + mode }

// IsModeSlug reports whether slug is one of the six mode presets.
func IsModeSlug(slug string) bool {
	for _, k := range []string{"image", "video"} {
		for _, m := range Modes {
			if slug == ModeSlug(k, m) {
				return true
			}
		}
	}
	return false
}

// ModePresets returns the six mode presets.
func ModePresets() []DefaultPreset {
	var out []DefaultPreset
	for _, d := range DefaultPresets() {
		if IsModeSlug(d.Slug) {
			out = append(out, d)
		}
	}
	return out
}

func modePresets() []struct {
	slug, kind, desc string
	params           any
} {
	type item = struct {
		slug, kind, desc string
		params           any
	}
	img := func(mode, desc string, maxSide int, q Quality, outs []ImageOutput, sharpen *Sharpen) item {
		return item{ModeSlug("image", mode), "image", desc, ImageParams{
			Kind:        "image",
			Resize:      Resize{MaxWidth: maxSide, MaxHeight: maxSide, Fit: "inside"},
			Sharpen:     sharpen,
			Outputs:     outs,
			Quality:     q,
			Placeholder: Placeholder{ThumbHash: true, DominantColor: true},
			Limits:      defaultImageLimits,
		}}
	}
	vid := func(mode, desc string, v VideoCodec, a AudioCodec) item {
		return item{ModeSlug("video", mode), "video", desc, VideoParams{
			Kind: "video", Video: v, Audio: a, Container: defaultContainer,
			Poster: Poster{AtSec: 1.0, Preset: "photo-lite", MaxBytes: 15360}, Limits: defaultVideoLimits,
		}}
	}
	hq := []ImageOutput{
		{Name: "main", Format: "avif", Effort: 4, Chroma: "4:4:4"},
		{Name: "main", Format: "webp", SmartSubsample: false},
	}
	return []item{
		img(ModeCompact, "Compact: good quality at a very small size (max side 1080, ≤ 30 KB)", 1080,
			Quality{Mode: "budget", MaxBytes: 30 << 10, QMax: 55, QMin: 25, DownscaleStep: 0.85, MinSide: 480},
			defaultOutputs, &Sharpen{Sigma: 0.6, M1: 0.5, M2: 1.5}),
		img(ModeBalanced, "Balanced: the golden middle (max side 1440, SSIM ≥ 0.975)", 1440,
			Quality{Mode: "ssim", Target: 0.975, QMin: 35, QMax: 65}, defaultOutputs, nil),
		img(ModeQuality, "Quality: ideal quality at the smallest size that achieves it (max side 2048, SSIM ≥ 0.99, 4:4:4)", 2048,
			Quality{Mode: "ssim", Target: 0.99, QMin: 45, QMax: 85}, hq, nil),
		vid(ModeCompact, "Compact: 480p H.264 CRF 30, ≤ 450 kbit/s, AAC 48k mono",
			VideoCodec{Codec: "h264", MaxWidth: 854, FPS: 30, CRF: 30, Maxrate: "450k", Bufsize: "900k", Preset: "slow", HW: "auto"},
			AudioCodec{Codec: "aac", Bitrate: "48k", Channels: 1, SampleRate: 44100}),
		vid(ModeBalanced, "Balanced: 720p H.264 CRF 26, ≤ 1.2 Mbit/s, AAC 96k stereo",
			VideoCodec{Codec: "h264", MaxWidth: 1280, FPS: 30, CRF: 26, Maxrate: "1200k", Bufsize: "2400k", Preset: "slow", HW: "auto"},
			AudioCodec{Codec: "aac", Bitrate: "96k", Channels: 2, SampleRate: 44100}),
		vid(ModeQuality, "Quality: 1080p H.264 CRF 20, ≤ 5 Mbit/s, AAC 160k stereo",
			VideoCodec{Codec: "h264", MaxWidth: 1920, FPS: 30, CRF: 20, Maxrate: "5000k", Bufsize: "10000k", Preset: "slow", HW: "auto"},
			AudioCodec{Codec: "aac", Bitrate: "160k", Channels: 2, SampleRate: 48000}),
	}
}
