package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// supportedFormats is used when limits.Accept is empty.
var supportedFormats = []string{FormatJPEG, FormatPNG, FormatWebP, FormatHEIC, FormatAVIF, FormatGIF}

// Probe implements Engine. The order of checks is cheapest first: size,
// magic bytes, accept list, structure scan, then `vipsheader` (header only)
// for dimensions and the pixel limit. No pixels are decoded.
func (e *VipsCLI) Probe(ctx context.Context, path string, limits preset.ImageLimits) (SourceInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	if err := e.acquire(ctx); err != nil {
		return SourceInfo{}, err
	}
	defer e.release()

	abs, err := filepath.Abs(path)
	if err != nil {
		return SourceInfo{}, engineErr("resolve source path", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return SourceInfo{}, engineErr("open source", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return SourceInfo{}, engineErr("stat source", err)
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: "source is not a regular file"}
	}
	if limits.MaxInputBytes > 0 && st.Size() > limits.MaxInputBytes {
		f.Close()
		return SourceInfo{}, &Error{Code: CodeTooLarge, Detail: fmt.Sprintf("source is %d bytes, limit is %d", st.Size(), limits.MaxInputBytes)}
	}
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	f.Close()
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return SourceInfo{}, engineErr("read source", err)
	}
	format := sniffFormat(head[:n])
	if format == "" {
		return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: "unrecognised image format"}
	}
	accept := limits.Accept
	if len(accept) == 0 {
		accept = supportedFormats
	}
	if !slices.Contains(accept, format) {
		return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: format + " is not accepted by this preset"}
	}
	if err := checkStructure(abs, format); err != nil {
		if errors.Is(err, errTruncated) {
			return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: format + " " + err.Error()}
		}
		return SourceInfo{}, engineErr("scan source", err)
	}

	in, cleanup, err := safeInputPath(abs, "")
	if err != nil {
		return SourceInfo{}, engineErr("prepare source path", err)
	}
	defer cleanup()
	var cpu int64
	h, err := e.readHeader(ctx, &cpu, in, true)
	if err != nil {
		if _, ok := AsError(err); ok {
			return SourceInfo{}, err
		}
		return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "cannot read image header", Err: err}
	}
	if want := expectedLoader[format]; !strings.HasPrefix(h.Loader, want) {
		return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: fmt.Sprintf("detected %s but libvips chose loader %q", format, h.Loader)}
	}
	if h.Width <= 0 || h.Height <= 0 {
		return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "image has no dimensions"}
	}
	w, hh := h.Width, h.Height
	if h.Orientation >= 5 { // 5..8 swap axes
		w, hh = hh, w
	}
	if limits.MaxInputPixels > 0 && int64(w)*int64(hh) > limits.MaxInputPixels {
		return SourceInfo{}, &Error{Code: CodeTooManyPixels, Detail: fmt.Sprintf("%dx%d is over the limit of %d pixels", w, hh, limits.MaxInputPixels)}
	}
	return SourceInfo{
		Path:        abs,
		Format:      format,
		ContentType: contentTypes[format],
		Bytes:       st.Size(),
		Width:       w,
		Height:      hh,
		Pages:       h.Pages,
	}, nil
}

// safeInputPath returns a path libvips can open literally. libvips parses
// "name.jpg[options]" as load options, so a path containing brackets is
// replaced by a symlink with a neutral name, created in dir (or a fresh
// temporary directory when dir is empty). The source itself is never
// modified.
func safeInputPath(abs, dir string) (string, func(), error) {
	if !strings.ContainsAny(abs, "[]") {
		return abs, func() {}, nil
	}
	cleanup := func() {}
	if dir == "" {
		d, err := os.MkdirTemp("", "kadr-probe-")
		if err != nil {
			return "", nil, err
		}
		dir = d
		cleanup = func() { os.RemoveAll(d) }
	}
	link := filepath.Join(dir, "_source")
	_ = os.Remove(link)
	if err := os.Symlink(abs, link); err != nil {
		cleanup()
		return "", nil, err
	}
	return link, cleanup, nil
}
