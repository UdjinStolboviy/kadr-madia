package image

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// The engine's intermediates are libvips native .v files: a fixed 64-byte
// header followed by the raw pixels (for uncoded images) and an XML
// metadata extension. Reading them in Go saves a child process per step
// (≈ 95 ms on macOS, ≈ 10–20 ms on Linux), which matters because the CLI
// engine pays that cost for every operation (docs/adr/0014).

// vHeaderLen is the size of the fixed .v header.
const vHeaderLen = 64

// Values from libvips' VipsBandFormat, VipsCoding and VipsInterpretation.
const (
	vFormatUchar = 0
	vCodingNone  = 0
	vTypeBW      = 1
	vTypeSRGB    = 22
)

// vHeader is the part of the .v header the engine uses.
type vHeader struct {
	W, H, Bands    int
	Format, Coding int
	Type           int
	order          binary.ByteOrder
}

func parseVHeader(b []byte) (vHeader, error) {
	if len(b) < 32 {
		return vHeader{}, errors.New("short .v header")
	}
	var bo binary.ByteOrder
	switch {
	case b[0] == 0xb6 && b[1] == 0xa6 && b[2] == 0xf2 && b[3] == 0x08:
		bo = binary.LittleEndian
	case b[0] == 0x08 && b[1] == 0xf2 && b[2] == 0xa6 && b[3] == 0xb6:
		bo = binary.BigEndian
	default:
		return vHeader{}, errors.New("not a vips .v file")
	}
	u := func(off int) int { return int(int32(bo.Uint32(b[off : off+4]))) }
	h := vHeader{W: u(4), H: u(8), Bands: u(12), Format: u(20), Coding: u(24), Type: u(28), order: bo}
	if h.W <= 0 || h.H <= 0 || h.Bands <= 0 {
		return vHeader{}, errors.New("invalid .v header")
	}
	return h, nil
}

func readVFileHeader(path string) (vHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return vHeader{}, err
	}
	defer f.Close()
	b := make([]byte, 32)
	if _, err := io.ReadFull(f, b); err != nil {
		return vHeader{}, fmt.Errorf("read .v header: %w", err)
	}
	return parseVHeader(b)
}

// readVHeader reads width, height and bands from the fixed header of a
// libvips native .v file, avoiding a vipsheader process for the engine's
// own intermediates.
func readVHeader(path string) (raster, error) {
	f, err := os.Open(path)
	if err != nil {
		return raster{}, err
	}
	defer f.Close()
	var b [32]byte
	n, err := io.ReadFull(f, b[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return raster{}, fmt.Errorf("read .v header: %w", err)
	}
	if n < 16 {
		return raster{}, errors.New("read .v header: file too short")
	}
	// Width, height and bands sit in the first 16 bytes; the rest of the
	// header is optional for this call.
	full := make([]byte, 32)
	copy(full, b[:n])
	h, err := parseVHeader(full)
	if err != nil {
		return raster{}, err
	}
	return raster{Path: path, W: h.W, H: h.H, Bands: h.Bands}, nil
}

// is8bitSRGB reports whether the header describes an uncoded 8-bit sRGB
// image with 3 or 4 bands, which every later step expects.
func (h vHeader) is8bitSRGB() bool {
	return h.Format == vFormatUchar && h.Coding == vCodingNone && h.Type == vTypeSRGB && (h.Bands == 3 || h.Bands == 4)
}

// errVUnsupported marks a .v file whose pixels cannot be read directly
// (not 8-bit, coded, or with an unexpected band count). Callers fall back
// to decoding through vips.
var errVUnsupported = errors.New("unsupported .v pixel layout")

// readVImage reads the pixels of an uncoded 8-bit .v file with 1–4 bands
// (gray, gray+alpha, RGB, RGBA) as non-premultiplied RGBA.
func readVImage(path string) (*rgbaPlane, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hb := make([]byte, vHeaderLen)
	if _, err := io.ReadFull(f, hb); err != nil {
		return nil, fmt.Errorf("read .v header: %w", err)
	}
	h, err := parseVHeader(hb)
	if err != nil {
		return nil, err
	}
	if h.Format != vFormatUchar || h.Coding != vCodingNone || h.Bands > 4 {
		return nil, errVUnsupported
	}
	n := h.W * h.H
	if n > 1<<28 {
		return nil, errVUnsupported
	}
	raw := make([]byte, n*h.Bands)
	if _, err := io.ReadFull(f, raw); err != nil {
		return nil, fmt.Errorf("read .v pixels: %w", err)
	}
	img := &rgbaPlane{W: h.W, H: h.H, Pix: make([]uint8, n*4)}
	px := img.Pix
	switch h.Bands {
	case 1:
		for i := range n {
			v := raw[i]
			px[4*i], px[4*i+1], px[4*i+2], px[4*i+3] = v, v, v, 255
		}
	case 2:
		for i := range n {
			v := raw[2*i]
			px[4*i], px[4*i+1], px[4*i+2], px[4*i+3] = v, v, v, raw[2*i+1]
		}
	case 3:
		for i := range n {
			px[4*i], px[4*i+1], px[4*i+2], px[4*i+3] = raw[3*i], raw[3*i+1], raw[3*i+2], 255
		}
	case 4:
		copy(px, raw)
	}
	return img, nil
}

// downRGBA shrinks img by area averaging so its long side is ≤ maxSide.
// Color is averaged weighted by alpha so transparent pixels do not bleed
// their hidden RGB into the result. Images already small enough are
// returned unchanged.
func downRGBA(img *rgbaPlane, maxSide int) *rgbaPlane {
	long := max(img.W, img.H)
	if long <= maxSide {
		return img
	}
	w := max(1, img.W*maxSide/long)
	h := max(1, img.H*maxSide/long)
	out := &rgbaPlane{W: w, H: h, Pix: make([]uint8, w*h*4)}
	sx, sy := float64(img.W)/float64(w), float64(img.H)/float64(h)
	for y := range h {
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		for x := range w {
			x0, x1 := float64(x)*sx, float64(x+1)*sx
			var r, g, b, a, area float64
			for iy := int(y0); iy < img.H && float64(iy) < y1; iy++ {
				wy := min(y1, float64(iy+1)) - max(y0, float64(iy))
				if wy <= 0 {
					continue
				}
				for ix := int(x0); ix < img.W && float64(ix) < x1; ix++ {
					wx := min(x1, float64(ix+1)) - max(x0, float64(ix))
					if wx <= 0 {
						continue
					}
					wgt := wx * wy
					i := (iy*img.W + ix) * 4
					al := float64(img.Pix[i+3]) * wgt
					r += float64(img.Pix[i]) * al
					g += float64(img.Pix[i+1]) * al
					b += float64(img.Pix[i+2]) * al
					a += al
					area += wgt
				}
			}
			o := (y*w + x) * 4
			if a > 0 {
				out.Pix[o] = uint8(min(255, r/a+0.5))
				out.Pix[o+1] = uint8(min(255, g/a+0.5))
				out.Pix[o+2] = uint8(min(255, b/a+0.5))
			}
			if area > 0 {
				out.Pix[o+3] = uint8(min(255, a/area+0.5))
			}
		}
	}
	return out
}

// lumaPercentiles returns the lo-th and hi-th percentile (0–100) of the
// luminance of img, matching `vips percent`, which normalises the
// cumulative histogram to integers 0..255 (truncating) and returns the
// first bin whose value exceeds pc·256/100. gray selects band 0 directly;
// otherwise
// luminance is computed like libvips' sRGB → B_W conversion (relative
// luminance of linear RGB, re-encoded with the sRGB curve).
func lumaPercentiles(img *rgbaPlane, gray bool, lo, hi float64) (float64, float64) {
	var hist [256]int
	n := img.W * img.H
	if gray {
		for i := range n {
			hist[img.Pix[4*i]]++
		}
	} else {
		for i := range n {
			p := img.Pix[4*i : 4*i+3]
			y := 0.2126*srgbToLinear[p[0]] + 0.7152*srgbToLinear[p[1]] + 0.0722*srgbToLinear[p[2]]
			hist[linearToSRGB(y)]++
		}
	}
	pick := func(pc float64) float64 {
		cum := 0
		for v, c := range hist {
			cum += c
			if math.Floor(float64(cum)*255/float64(n)) > pc*256/100 {
				return float64(v)
			}
		}
		return 255
	}
	return pick(lo), pick(hi)
}
