package image

import (
	"errors"
	"fmt"
	"math"
)

// This file is a Go port of the ThumbHash reference encoder by Evan Wallace
// (https://github.com/evanw/thumbhash, MIT), kept dependency-free. The
// output matches the reference rgbaToThumbHash for the same RGBA input,
// except that an AC term which is mathematically zero (normalised to 7.5)
// may round to 7 or 8 depending on last-ulp differences in cos(). Products are wrapped in explicit float64 conversions where
// they feed an addition: the Go spec otherwise allows fused multiply-add
// (e.g. on arm64), which changes rounding and breaks byte equality with the
// JavaScript reference.

// jsRound mirrors JavaScript Math.round (round half up), which the
// reference implementation uses.
func jsRound(x float64) int { return int(math.Floor(x + 0.5)) }

// rgbaToThumbHash encodes a w×h non-premultiplied RGBA image (w, h ≤ 100).
func rgbaToThumbHash(w, h int, rgba []byte) ([]byte, error) {
	if w <= 0 || h <= 0 || w > 100 || h > 100 {
		return nil, fmt.Errorf("thumbhash: %dx%d does not fit in 100x100", w, h)
	}
	if len(rgba) < w*h*4 {
		return nil, errors.New("thumbhash: pixel buffer too small")
	}
	n := w * h

	// Average color, weighted by alpha.
	var avgR, avgG, avgB, avgA float64
	for i, j := 0, 0; i < n; i, j = i+1, j+4 {
		alpha := float64(rgba[j+3]) / 255
		avgR += float64(alpha / 255 * float64(rgba[j]))
		avgG += float64(alpha / 255 * float64(rgba[j+1]))
		avgB += float64(alpha / 255 * float64(rgba[j+2]))
		avgA += alpha
	}
	if avgA > 0 {
		avgR /= avgA
		avgG /= avgA
		avgB /= avgA
	}

	hasAlpha := avgA < float64(n)
	lLimit := 7
	if hasAlpha {
		lLimit = 5 // fewer luminance bits when there is alpha
	}
	maxWH := float64(max(w, h))
	lx := max(1, jsRound(float64(lLimit*w)/maxWH))
	ly := max(1, jsRound(float64(lLimit*h)/maxWH))

	l := make([]float64, n) // luminance
	p := make([]float64, n) // yellow - blue
	q := make([]float64, n) // red - green
	a := make([]float64, n) // alpha

	// RGBA → LPQA, composited atop the average color.
	for i, j := 0, 0; i < n; i, j = i+1, j+4 {
		alpha := float64(rgba[j+3]) / 255
		r := float64(avgR*(1-alpha)) + float64(alpha/255*float64(rgba[j]))
		g := float64(avgG*(1-alpha)) + float64(alpha/255*float64(rgba[j+1]))
		b := float64(avgB*(1-alpha)) + float64(alpha/255*float64(rgba[j+2]))
		l[i] = (r + g + b) / 3
		p[i] = (r+g)/2 - b
		q[i] = r - g
		a[i] = alpha
	}

	// DCT into a DC term and normalized AC terms.
	encodeChannel := func(channel []float64, nx, ny int) (dc float64, ac []float64, scale float64) {
		fx := make([]float64, w)
		for cy := range ny {
			for cx := 0; cx*ny < nx*(ny-cy); cx++ {
				var f float64
				for x := range w {
					fx[x] = math.Cos(math.Pi / float64(w) * float64(cx) * (float64(x) + 0.5))
				}
				for y := range h {
					fy := math.Cos(math.Pi / float64(h) * float64(cy) * (float64(y) + 0.5))
					for x := range w {
						f += float64(channel[x+y*w] * fx[x] * fy)
					}
				}
				f /= float64(n)
				if cx > 0 || cy > 0 {
					ac = append(ac, f)
					scale = math.Max(scale, math.Abs(f))
				} else {
					dc = f
				}
			}
		}
		if scale > 0 {
			for i := range ac {
				ac[i] = 0.5 + float64(0.5/scale*ac[i])
			}
		}
		return dc, ac, scale
	}

	lDC, lAC, lScale := encodeChannel(l, max(3, lx), max(3, ly))
	pDC, pAC, pScale := encodeChannel(p, 3, 3)
	qDC, qAC, qScale := encodeChannel(q, 3, 3)
	var aDC, aScale float64
	var aAC []float64
	if hasAlpha {
		aDC, aAC, aScale = encodeChannel(a, 5, 5)
	}

	isLandscape := w > h
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	header24 := jsRound(63*lDC) |
		jsRound(31.5+float64(31.5*pDC))<<6 |
		jsRound(31.5+float64(31.5*qDC))<<12 |
		jsRound(31*lScale)<<18 |
		b2i(hasAlpha)<<23
	lxy := lx
	if isLandscape {
		lxy = ly
	}
	header16 := lxy |
		jsRound(63*pScale)<<3 |
		jsRound(63*qScale)<<9 |
		b2i(isLandscape)<<15

	hash := []byte{
		byte(header24), byte(header24 >> 8), byte(header24 >> 16),
		byte(header16), byte(header16 >> 8),
	}
	acStart := 5
	if hasAlpha {
		acStart = 6
		hash = append(hash, byte(jsRound(15*aDC)|jsRound(15*aScale)<<4))
	}
	channels := [][]float64{lAC, pAC, qAC}
	if hasAlpha {
		channels = append(channels, aAC)
	}
	acIndex := 0
	for _, ac := range channels {
		for _, f := range ac {
			idx := acStart + acIndex>>1
			for len(hash) <= idx {
				hash = append(hash, 0)
			}
			hash[idx] |= byte(jsRound(15*f) << ((acIndex & 1) << 2))
			acIndex++
		}
	}
	return hash, nil
}

// thumbHashAverageRGBA decodes the average color stored in a ThumbHash
// (reference thumbHashToAverageRGBA). Used by tests.
func thumbHashAverageRGBA(hash []byte) (r, g, b, a float64) {
	header := int(hash[0]) | int(hash[1])<<8 | int(hash[2])<<16
	l := float64(header&63) / 63
	p := float64((header>>6)&63)/31.5 - 1
	q := float64((header>>12)&63)/31.5 - 1
	hasAlpha := header>>23 != 0
	a = 1
	if hasAlpha {
		a = float64(hash[5]&15) / 15
	}
	bb := l - 2.0/3.0*p
	rr := (3*l - bb + q) / 2
	gg := rr - q
	clamp := func(x float64) float64 { return math.Max(0, math.Min(1, x)) }
	return clamp(rr), clamp(gg), clamp(bb), a
}
