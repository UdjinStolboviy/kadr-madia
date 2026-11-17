package image

import (
	"fmt"
	goimage "image"
	"image/draw"
	_ "image/gif" // decoders for Go-side analysis of vips output
	_ "image/jpeg"
	_ "image/png"
	"os"
)

// SSIM in Go
//
// The `ssim` quality mode needs a perceptual comparison between the
// intermediate raster and an encoded candidate. libvips has no SSIM
// operation, so the engine asks vips to decode both to PNG and computes
// SSIM here:
//
//  1. decode PNG, composite alpha over white, convert to luma (BT.601);
//  2. box-downscale by an integer factor so the long side is ≤ ssimMaxSide
//     (bounds the cost and roughly matches typical viewing scale);
//  3. mean SSIM over 8×8 windows on a 4-pixel stride (Wang et al. 2004
//     constants, uniform window) computed with summed-area tables.
//
// Luma-only SSIM ignores chroma loss; AVIF/WebP chroma subsampling is
// controlled separately by the preset, so this is an acceptable trade-off.

const ssimMaxSide = 1024

// grayPlane is an 8-bit-range luma image stored as float64.
type grayPlane struct {
	W, H int
	Pix  []float64
}

// rgbaPlane is a decoded non-premultiplied RGBA image.
type rgbaPlane struct {
	W, H int
	Pix  []uint8 // RGBA, 4 bytes per pixel
}

func decodeFile(path string) (*rgbaPlane, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := goimage.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	nrgba := goimage.NewNRGBA(goimage.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(nrgba, nrgba.Bounds(), img, b.Min, draw.Src)
	return &rgbaPlane{W: b.Dx(), H: b.Dy(), Pix: nrgba.Pix}, nil
}

// luma converts to gray, compositing alpha over white so transparent
// regions compare equal regardless of their hidden RGB values.
func (p *rgbaPlane) luma() *grayPlane {
	g := &grayPlane{W: p.W, H: p.H, Pix: make([]float64, p.W*p.H)}
	for i := range g.Pix {
		r, gg, b, a := float64(p.Pix[4*i]), float64(p.Pix[4*i+1]), float64(p.Pix[4*i+2]), float64(p.Pix[4*i+3])/255
		y := 0.299*r + 0.587*gg + 0.114*b
		g.Pix[i] = y*a + 255*(1-a)
	}
	return g
}

// boxDown downscales by an integer factor (k ≥ 1), averaging k×k blocks.
func (g *grayPlane) boxDown(k int) *grayPlane {
	if k <= 1 {
		return g
	}
	w, h := g.W/k, g.H/k
	if w < 1 || h < 1 {
		return g
	}
	out := &grayPlane{W: w, H: h, Pix: make([]float64, w*h)}
	inv := 1 / float64(k*k)
	for y := range h {
		for x := range w {
			var s float64
			for dy := range k {
				row := (y*k + dy) * g.W
				for dx := range k {
					s += g.Pix[row+x*k+dx]
				}
			}
			out.Pix[y*w+x] = s * inv
		}
	}
	return out
}

// resample resizes to w×h with area averaging (bilinear when enlarging).
// Used by tests to bring a reference to a rendition's size.
func (g *grayPlane) resample(w, h int) *grayPlane {
	if w == g.W && h == g.H {
		return g
	}
	out := &grayPlane{W: w, H: h, Pix: make([]float64, w*h)}
	sx, sy := float64(g.W)/float64(w), float64(g.H)/float64(h)
	for y := range h {
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		for x := range w {
			x0, x1 := float64(x)*sx, float64(x+1)*sx
			var sum, area float64
			for iy := int(y0); iy < g.H && float64(iy) < y1; iy++ {
				wy := min(y1, float64(iy+1)) - max(y0, float64(iy))
				if wy <= 0 {
					continue
				}
				for ix := int(x0); ix < g.W && float64(ix) < x1; ix++ {
					wx := min(x1, float64(ix+1)) - max(x0, float64(ix))
					if wx <= 0 {
						continue
					}
					sum += g.Pix[iy*g.W+ix] * wx * wy
					area += wx * wy
				}
			}
			if area > 0 {
				out.Pix[y*w+x] = sum / area
			}
		}
	}
	return out
}

// ssimFactor returns the integer downscale factor for a w×h image.
func ssimFactor(w, h int) int {
	long := max(w, h)
	return (long + ssimMaxSide - 1) / ssimMaxSide
}

// ssim returns the mean SSIM of two equally sized planes. If sizes differ by
// a few pixels (rounding), the common top-left region is compared.
func ssim(a, b *grayPlane) float64 {
	w, h := min(a.W, b.W), min(a.H, b.H)
	const win, stride = 8, 4
	if w < win || h < win {
		return ssimGlobal(a, b, w, h)
	}
	// Summed-area tables of x, y, x², y², xy.
	sw := w + 1
	sa := make([]float64, sw*(h+1))
	sb := make([]float64, sw*(h+1))
	saa := make([]float64, sw*(h+1))
	sbb := make([]float64, sw*(h+1))
	sab := make([]float64, sw*(h+1))
	for y := range h {
		var ra, rb, raa, rbb, rab float64
		for x := range w {
			va, vb := a.Pix[y*a.W+x], b.Pix[y*b.W+x]
			ra += va
			rb += vb
			raa += va * va
			rbb += vb * vb
			rab += va * vb
			i := (y+1)*sw + x + 1
			up := y*sw + x + 1
			sa[i] = sa[up] + ra
			sb[i] = sb[up] + rb
			saa[i] = saa[up] + raa
			sbb[i] = sbb[up] + rbb
			sab[i] = sab[up] + rab
		}
	}
	rect := func(t []float64, x, y int) float64 {
		x1, y1 := x+win, y+win
		return t[y1*sw+x1] - t[y*sw+x1] - t[y1*sw+x] + t[y*sw+x]
	}
	const c1 = (0.01 * 255) * (0.01 * 255)
	const c2 = (0.03 * 255) * (0.03 * 255)
	n := float64(win * win)
	var total float64
	var count int
	for y := 0; y+win <= h; y += stride {
		for x := 0; x+win <= w; x += stride {
			ma, mb := rect(sa, x, y)/n, rect(sb, x, y)/n
			va := rect(saa, x, y)/n - ma*ma
			vb := rect(sbb, x, y)/n - mb*mb
			cov := rect(sab, x, y)/n - ma*mb
			total += ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			count++
		}
	}
	return total / float64(count)
}

func ssimGlobal(a, b *grayPlane, w, h int) float64 {
	if w == 0 || h == 0 {
		return 0
	}
	var sa, sb, saa, sbb, sab float64
	for y := range h {
		for x := range w {
			va, vb := a.Pix[y*a.W+x], b.Pix[y*b.W+x]
			sa += va
			sb += vb
			saa += va * va
			sbb += vb * vb
			sab += va * vb
		}
	}
	n := float64(w * h)
	ma, mb := sa/n, sb/n
	va, vb, cov := saa/n-ma*ma, sbb/n-mb*mb, sab/n-ma*mb
	const c1 = (0.01 * 255) * (0.01 * 255)
	const c2 = (0.03 * 255) * (0.03 * 255)
	return ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
}
