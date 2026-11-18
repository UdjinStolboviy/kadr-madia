package preset

// FileParams is params_json for kind=file presets (documents: PDF, Office,
// OpenDocument, text). Files are validated by content, stored as they are
// and — for PDFs — optionally optimised; a thumbnail of the first page is
// produced for PDFs.
type FileParams struct {
	Kind         string       `json:"kind"`
	Limits       FileLimits   `json:"limits"`
	Optimize     FileOptimize `json:"optimize"`
	Thumbnail    FileThumb    `json:"thumbnail"`
	RetainOrigin bool         `json:"retainOriginal"`
}

// FileLimits guard input size and formats.
type FileLimits struct {
	MaxInputBytes int64    `json:"maxInputBytes"`
	Accept        []string `json:"accept"` // pdf docx xlsx pptx odt ods odp rtf txt csv
}

// Optimization strategies of FileOptimize.Strategy.
const (
	FileStrategyNone       = "none"       // store as uploaded
	FileStrategyLossless   = "lossless"   // PDF: qpdf recompression + linearization, no visible change
	FileStrategyDownsample = "downsample" // PDF: Ghostscript re-encodes images at ImageDPI / JPEGQuality
)

// FileOptimize describes PDF optimisation. Signed, encrypted and PDF/A files
// are never rewritten, and a result that is not at least MinSavingsPercent
// smaller is discarded in favour of the original.
type FileOptimize struct {
	Strategy          string `json:"strategy"`
	ImageDPI          int    `json:"imageDpi,omitempty"`    // downsample target resolution
	JPEGQuality       int    `json:"jpegQuality,omitempty"` // 1–100
	Linearize         bool   `json:"linearize"`             // "fast web view"
	MinSavingsPercent int    `json:"minSavingsPercent"`
}

// FileThumb controls the first-page thumbnail of PDFs (compressed with the
// image budget search to MaxBytes; AVIF + WebP).
type FileThumb struct {
	Enabled  bool `json:"enabled"`
	MaxSide  int  `json:"maxSide"`
	MaxBytes int  `json:"maxBytes"`
	// Formats as for video posters: empty means avif + webp.
	Formats []string `json:"formats,omitempty"`
}

// DefaultFileFormats are the document formats accepted by the built-in presets.
var DefaultFileFormats = []string{"pdf", "docx", "xlsx", "pptx", "odt", "ods", "odp", "rtf", "txt", "csv"}

var defaultFileLimits = FileLimits{MaxInputBytes: 100 << 20, Accept: DefaultFileFormats}

func filePresets() []struct {
	slug, kind, desc string
	params           any
} {
	type item = struct {
		slug, kind, desc string
		params           any
	}
	f := func(slug, desc string, o FileOptimize, th FileThumb) item {
		return item{slug, "file", desc, FileParams{Kind: "file", Limits: defaultFileLimits, Optimize: o, Thumbnail: th}}
	}
	return []item{
		f(ModeSlug("file", ModeCompact), "Compact: PDF images re-encoded at 110 dpi / JPEG 60, linearized; thumbnail ≤ 12 KB",
			FileOptimize{Strategy: FileStrategyDownsample, ImageDPI: 110, JPEGQuality: 60, Linearize: true, MinSavingsPercent: 5},
			FileThumb{Enabled: true, MaxSide: 480, MaxBytes: 12 << 10}),
		f(ModeSlug("file", ModeBalanced), "Balanced: PDF images at 150 dpi / JPEG 75, linearized; thumbnail ≤ 20 KB",
			FileOptimize{Strategy: FileStrategyDownsample, ImageDPI: 150, JPEGQuality: 75, Linearize: true, MinSavingsPercent: 5},
			FileThumb{Enabled: true, MaxSide: 640, MaxBytes: 20 << 10}),
		f(ModeSlug("file", ModeQuality), "Quality: lossless PDF recompression (no visible change), linearized; thumbnail ≤ 30 KB",
			FileOptimize{Strategy: FileStrategyLossless, Linearize: true, MinSavingsPercent: 3},
			FileThumb{Enabled: true, MaxSide: 800, MaxBytes: 30 << 10}),
		f("file-original", "Documents stored exactly as uploaded (validated by content); PDF thumbnail ≤ 20 KB",
			FileOptimize{Strategy: FileStrategyNone}, FileThumb{Enabled: true, MaxSide: 640, MaxBytes: 20 << 10}),
	}
}
