package file

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Config configures the document engine.
type Config struct {
	Ghostscript string // default "gs"; empty result of LookPath disables PDF rendering/downsampling
	QPDF        string // default "qpdf"
	PDFToText   string // default "pdftotext"; empty result disables PDF text extraction
	Nice        bool
	// Timeouts per external command.
	OptimizeTimeout time.Duration // default 3m
	ThumbTimeout    time.Duration // default 45s
	ProbeTimeout    time.Duration // default 30s
	TextTimeout     time.Duration // default 45s
}

// Tools is the document engine (Engine implementation).
type Tools struct {
	cfg       Config
	gs        string
	qpdf      string
	pdftotext string
	nice      string
}

var _ Engine = (*Tools)(nil)

// New builds the engine. It never fails: missing tools only disable the
// features that need them (see HasGhostscript / HasQPDF / HasPDFToText).
func New(cfg Config) *Tools {
	if cfg.Ghostscript == "" {
		cfg.Ghostscript = "gs"
	}
	if cfg.QPDF == "" {
		cfg.QPDF = "qpdf"
	}
	if cfg.PDFToText == "" {
		cfg.PDFToText = "pdftotext"
	}
	if cfg.OptimizeTimeout <= 0 {
		cfg.OptimizeTimeout = 3 * time.Minute
	}
	if cfg.ThumbTimeout <= 0 {
		cfg.ThumbTimeout = 45 * time.Second
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 30 * time.Second
	}
	if cfg.TextTimeout <= 0 {
		cfg.TextTimeout = 45 * time.Second
	}
	t := &Tools{cfg: cfg}
	t.gs, _ = exec.LookPath(cfg.Ghostscript)
	t.qpdf, _ = exec.LookPath(cfg.QPDF)
	t.pdftotext, _ = exec.LookPath(cfg.PDFToText)
	if cfg.Nice {
		t.nice, _ = exec.LookPath("nice")
	}
	return t
}

// HasGhostscript reports whether PDF thumbnails and downsampling are available.
func (t *Tools) HasGhostscript() bool { return t.gs != "" }

// HasQPDF reports whether lossless recompression and linearization are available.
func (t *Tools) HasQPDF() bool { return t.qpdf != "" }

// HasPDFToText reports whether PDF text extraction is available.
func (t *Tools) HasPDFToText() bool { return t.pdftotext != "" }

// run executes bin with a minimal environment in dir. okCodes are exit codes
// treated as success (qpdf exits 3 on warnings but still writes the output).
func (t *Tools) run(ctx context.Context, cpu *int64, dir string, timeout time.Duration, bin string, args []string, okCodes ...int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{bin}, args...)
	if t.nice != "" {
		argv = append([]string{t.nice, "-n", "10"}, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "LC_ALL=C"}
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ps := cmd.ProcessState; ps != nil && cpu != nil {
		atomic.AddInt64(cpu, (ps.UserTime() + ps.SystemTime()).Milliseconds())
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if ee, ok := err.(*exec.ExitError); ok {
			for _, c := range okCodes {
				if ee.ExitCode() == c {
					return out.String(), nil
				}
			}
		}
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return "", fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, msg)
	}
	return out.String(), nil
}

// gsBase are the flags every Ghostscript invocation gets: sandboxed file
// access, no prompts, no output noise.
var gsBase = []string{"-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-dNOPROMPT"}

// pageCount returns the page count of a PDF through Ghostscript (0, nil when
// Ghostscript is missing).
func (t *Tools) pageCount(ctx context.Context, cpu *int64, dir, pdf string) (int, error) {
	if t.gs == "" {
		return 0, nil
	}
	// The path is passed through a PostScript string: escape its delimiters.
	esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(pdf)
	args := append(append([]string{}, gsBase...), "--permit-file-read="+pdf, "-dNODISPLAY",
		"-c", fmt.Sprintf("(%s) (r) file runpdfbegin pdfpagecount = quit", esc))
	out, err := t.run(ctx, cpu, dir, t.cfg.ProbeTimeout, t.gs, args)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("unexpected page count output %q", strings.TrimSpace(out))
	}
	return n, nil
}

// thumbnail renders page 1 of pdf to a PNG.
func (t *Tools) thumbnail(ctx context.Context, cpu *int64, dir, pdf, png string, maxSide int) error {
	// A4/Letter at this resolution gives a long side a bit above maxSide.
	dpi := 100
	if maxSide > 0 {
		dpi = max(48, min(150, maxSide*72/700))
	}
	args := append(append([]string{}, gsBase...), "-sDEVICE=png16m", "-r"+strconv.Itoa(dpi),
		"-dFirstPage=1", "-dLastPage=1", "-dUseCropBox", "-dTextAlphaBits=4", "-dGraphicsAlphaBits=4",
		"-sOutputFile="+png, pdf)
	if _, err := t.run(ctx, cpu, dir, t.cfg.ThumbTimeout, t.gs, args); err != nil {
		return err
	}
	st, err := os.Stat(png)
	if err != nil || st.Size() == 0 {
		return fmt.Errorf("ghostscript produced no thumbnail")
	}
	return nil
}

// qfactor maps a JPEG quality (1–100) to Ghostscript's distiller QFactor.
func qfactor(q int) string {
	switch {
	case q >= 90:
		return "0.15"
	case q >= 80:
		return "0.40"
	case q >= 70:
		return "0.76"
	case q >= 60:
		return "1.30"
	default:
		return "2.40"
	}
}

func (t *Tools) downsample(ctx context.Context, cpu *int64, dir, in, out string, o preset.FileOptimize) error {
	dpi := o.ImageDPI
	if dpi < 36 || dpi > 600 {
		dpi = 150
	}
	mono := min(dpi*2, 300)
	q := qfactor(o.JPEGQuality)
	dict := fmt.Sprintf("<< /ColorACSImageDict << /QFactor %s /Blend 1 /HSamples [2 1 1 2] /VSamples [2 1 1 2] >> "+
		"/GrayACSImageDict << /QFactor %s /Blend 1 /HSamples [1 1 1 1] /VSamples [1 1 1 1] >> >> setdistillerparams", q, q)
	args := append(append([]string{}, gsBase...),
		"-sDEVICE=pdfwrite", "-dCompatibilityLevel=1.6", "-dDetectDuplicateImages=true", "-dCompressFonts=true", "-dSubsetFonts=true",
		"-dAutoFilterColorImages=false", "-dAutoFilterGrayImages=false", "-dColorImageFilter=/DCTEncode", "-dGrayImageFilter=/DCTEncode",
		"-dDownsampleColorImages=true", "-dColorImageDownsampleType=/Bicubic", "-dColorImageResolution="+strconv.Itoa(dpi),
		"-dDownsampleGrayImages=true", "-dGrayImageDownsampleType=/Bicubic", "-dGrayImageResolution="+strconv.Itoa(dpi),
		"-dDownsampleMonoImages=true", "-dMonoImageDownsampleType=/Subsample", "-dMonoImageResolution="+strconv.Itoa(mono),
		"-sOutputFile="+out, "-c", dict, "-f", in)
	_, err := t.run(ctx, cpu, dir, t.cfg.OptimizeTimeout, t.gs, args)
	return err
}

func (t *Tools) qpdfRewrite(ctx context.Context, cpu *int64, dir, in, out string, linearize bool) error {
	args := []string{"--object-streams=generate", "--compress-streams=y", "--recompress-flate", "--compression-level=9"}
	if linearize {
		args = append(args, "--linearize")
	}
	args = append(args, in, out)
	_, err := t.run(ctx, cpu, dir, t.cfg.OptimizeTimeout, t.qpdf, args, 3) // 3 = succeeded with warnings
	return err
}
