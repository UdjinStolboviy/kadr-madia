package file

import (
	"context"
	"errors"
	"fmt"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Probe implements Engine.
func (t *Tools) Probe(ctx context.Context, path, name string, limits preset.FileLimits) (SourceInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return SourceInfo{}, &Error{Code: CodeEngineFailure, Detail: "source is not readable", Retryable: true, Err: err}
	}
	if st.Size() == 0 {
		return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "the file is empty"}
	}
	if limits.MaxInputBytes > 0 && st.Size() > limits.MaxInputBytes {
		return SourceInfo{}, &Error{Code: CodeTooLarge, Detail: fmt.Sprintf("file is %d bytes, limit is %d", st.Size(), limits.MaxInputBytes)}
	}
	format, err := sniff(path, name)
	if err != nil {
		if errors.Is(err, errNotDocument) {
			return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: err.Error()}
		}
		return SourceInfo{}, &Error{Code: CodeEngineFailure, Detail: "could not inspect the file", Retryable: true, Err: err}
	}
	if len(limits.Accept) > 0 && !slices.Contains(limits.Accept, format) {
		return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: fmt.Sprintf("%s documents are not accepted by this preset (accepted: %s)", format, strings.Join(limits.Accept, ", "))}
	}
	info := SourceInfo{Path: path, Format: format, ContentType: ContentTypes[format], Bytes: st.Size()}
	if format == "pdf" {
		m, err := scanPDF(path)
		if err != nil {
			return SourceInfo{}, &Error{Code: CodeEngineFailure, Detail: "could not inspect the PDF", Retryable: true, Err: err}
		}
		if !m.HasEOF {
			return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "the PDF is truncated (no %%EOF marker)"}
		}
		info.Encrypted, info.Signed, info.PDFA = m.Encrypted, m.Signed, m.PDFA
		if !info.Encrypted && t.gs != "" {
			var cpu int64
			dir := filepath.Dir(path)
			n, err := t.pageCount(ctx, &cpu, dir, path)
			switch {
			case err == nil:
				info.Pages = n
			case ctx.Err() != nil:
				return SourceInfo{}, ctx.Err()
			default:
				// Ghostscript could not parse it at all: treat as corrupt.
				return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "the PDF cannot be read", Err: err}
			}
			if info.Pages <= 0 {
				return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "the PDF has no pages"}
			}
		}
	}
	return info, nil
}

// Process implements Engine.
func (t *Tools) Process(ctx context.Context, src SourceInfo, p preset.FileParams, workDir string) (*Output, error) {
	var cpu int64
	out := &Output{Main: MainFile{Path: src.Path, Format: src.Format, ContentType: src.ContentType, Bytes: src.Bytes,
		OriginalBytes: src.Bytes, Skipped: "disabled"}}
	if src.Format != "pdf" {
		out.Main.Skipped = "not_pdf"
		return out, nil
	}

	// Thumbnail of the first page (never rewritten input; encrypted PDFs cannot be rendered).
	if p.Thumbnail.Enabled && t.gs != "" && !src.Encrypted {
		png := filepath.Join(workDir, "thumb.png")
		if err := t.thumbnail(ctx, &cpu, workDir, src.Path, png, p.Thumbnail.MaxSide); err == nil {
			out.ThumbPath = png
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		} // a failed thumbnail is not fatal: the document is still stored
	}

	strategy := p.Optimize.Strategy
	switch {
	case strategy == "" || strategy == "none":
		out.Main.Skipped = "disabled"
	case src.Signed:
		out.Main.Skipped = "signed"
	case src.Encrypted:
		out.Main.Skipped = "encrypted"
	case src.PDFA:
		out.Main.Skipped = "pdfa"
	default:
		t.optimize(ctx, &cpu, workDir, src, p, out)
	}
	out.CPUMillis = cpu
	return out, nil
}

// optimize rewrites the PDF and keeps the result only if it is valid (same
// page count) and at least MinSavingsPercent smaller. Any failure leaves the
// original in place: optimisation is best-effort and never fails an upload.
func (t *Tools) optimize(ctx context.Context, cpu *int64, dir string, src SourceInfo, p preset.FileParams, out *Output) {
	o := p.Optimize
	cand := filepath.Join(dir, "optimized.pdf")
	var err error
	switch o.Strategy {
	case "downsample":
		if t.gs == "" {
			out.Main.Skipped = "tool_missing"
			return
		}
		err = t.downsample(ctx, cpu, dir, src.Path, cand, o)
		if err == nil && o.Linearize && t.qpdf != "" {
			lin := filepath.Join(dir, "linearized.pdf")
			if lerr := t.qpdfRewrite(ctx, cpu, dir, cand, lin, true); lerr == nil {
				_ = os.Rename(lin, cand)
			}
		}
	case "lossless":
		if t.qpdf == "" {
			out.Main.Skipped = "tool_missing"
			return
		}
		err = t.qpdfRewrite(ctx, cpu, dir, src.Path, cand, o.Linearize)
	default:
		out.Main.Skipped = "disabled"
		return
	}
	if err != nil {
		out.Main.Skipped = "failed"
		return
	}
	st, err := os.Stat(cand)
	if err != nil || st.Size() == 0 {
		out.Main.Skipped = "failed"
		return
	}
	minSave := o.MinSavingsPercent
	if minSave < 0 {
		minSave = 0
	}
	if st.Size() >= src.Bytes || (src.Bytes-st.Size())*100 < src.Bytes*int64(minSave) {
		out.Main.Skipped = "no_savings"
		_ = os.Remove(cand)
		return
	}
	if !validRewrite(ctx, t, cpu, dir, cand, src.Pages) {
		out.Main.Skipped = "failed"
		_ = os.Remove(cand)
		return
	}
	out.Main = MainFile{Path: cand, Format: "pdf", ContentType: ContentTypes["pdf"], Bytes: st.Size(), OriginalBytes: src.Bytes, Optimized: true}
}

// validRewrite checks the rewritten PDF: PDF header/trailer and, when the
// page count of the source is known, the same number of pages.
func validRewrite(ctx context.Context, t *Tools, cpu *int64, dir, path string, wantPages int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil || string(head[:5]) != "%PDF-" {
		return false
	}
	m, err := scanPDF(path)
	if err != nil || !m.HasEOF {
		return false
	}
	if wantPages > 0 {
		n, err := t.pageCount(ctx, cpu, dir, path)
		return err == nil && n == wantPages
	}
	return true
}
