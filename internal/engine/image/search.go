package image

import (
	"math"
)

// Quality search (docs/adr/0014-image-engine-performance.md).
//
// Every encode is a child process, so the search is designed to need as
// few as possible. It relies on two empirical properties of AVIF (aom) and
// WebP at the qualities presets use:
//
//   - size grows monotonically with q, and log(bytes) is roughly linear in
//     q (≈ 4–5 % per q step for AVIF, ≈ 1–2 % for WebP);
//   - size is roughly proportional to the pixel count at a fixed q, and
//     never smaller than that proportion predicts when the raster shrinks
//     (fewer pixels carry more detail each).
//
// searchQ is a safeguarded secant search on log(bytes): the first probe is
// the caller's guess (qMax at full size, which fits outright for most small
// or smooth images), each next probe is the q the model predicts lands just
// under the budget, and a bisection step is forced whenever a model step
// fails to halve the bracket, so the worst case stays logarithmic.

const (
	// searchTol stops the search once the highest fitting q is known to
	// within this many steps (the chosen q is at most searchTol-1 below the
	// true optimum).
	searchTol = 3
	// searchByteSlack also stops it once a fitting encode uses at least
	// (1 - searchByteSlack) of the budget: the remaining q steps would buy
	// a few percent of bytes for a full encode.
	searchByteSlack = 0.08
	// searchAim is how far under the budget the model aims, so a predicted
	// q usually lands on the fitting side.
	searchAim = 0.03
	// maxSearchProbes bounds one searchQ call (a bisection over q 1–100
	// needs 7).
	maxSearchProbes = 8

	// Default log-size slopes per q step, used until two samples exist.
	slopeAVIF = 0.045
	slopeWebP = 0.02
)

// sample is one encode at quality q that produced n bytes.
type sample struct {
	q int
	n int64
}

// searchResult brackets the highest fitting quality: fit is the highest q
// known to fit (lo-1 when none does) and fail the lowest q known not to fit
// (hi+1 when none failed).
type searchResult struct {
	fit, fail int
	fitBytes  int64
	samples   []sample
}

// found reports whether some q in [lo, hi] fits.
func (s searchResult) found(lo int) bool { return s.fit >= lo }

// searchQ finds (within searchTol) the highest q in [lo, hi] whose encode
// fits maxBytes. try encodes at q and returns the size; start is the first
// q to try and slope the default log-size slope per q step.
func searchQ(lo, hi, start int, maxBytes int64, slope float64, try func(q int) (int64, error)) (searchResult, error) {
	res := searchResult{fit: lo - 1, fail: hi + 1}
	q := max(lo, min(hi, start))
	prevWidth := hi - lo + 1
	for i := range maxSearchProbes {
		n, err := try(q)
		if err != nil {
			return res, err
		}
		res.samples = append(res.samples, sample{q, n})
		if n <= maxBytes {
			res.fit, res.fitBytes = q, n
		} else {
			res.fail = q
		}
		if res.fit == hi || res.fail == lo {
			break
		}
		if res.found(lo) && (res.fail-res.fit <= searchTol || float64(res.fitBytes) >= (1-searchByteSlack)*float64(maxBytes)) {
			break
		}
		width := res.fail - res.fit - 1 // untested candidates in the bracket
		if width <= 0 {
			break
		}
		// Bisection safeguard: from the fifth probe on, a probe that
		// did not halve the bracket means the model is not converging
		// (noise, or a curve far from log-linear), so bisect.
		stalled := i >= 4 && width > prevWidth/2
		prevWidth = width
		next, ok := predictQ(res, maxBytes, slope)
		if !ok || stalled {
			next = (res.fit + res.fail) / 2
		}
		q = max(res.fit+1, min(res.fail-1, next))
	}
	return res, nil
}

// predictQ returns the q the log-linear model expects to land just under
// the budget. The model is local: a line through the two samples nearest
// the target in log(bytes) (real curves are convex, so distant samples
// would under-predict), or through the nearest sample with the default
// slope while only one sample exists.
func predictQ(res searchResult, maxBytes int64, slope float64) (int, bool) {
	target := float64(maxBytes) * (1 - searchAim)
	dist := func(s sample) float64 { return math.Abs(math.Log(float64(max(s.n, 1))) - math.Log(target)) }
	a := res.samples[0]
	for _, s := range res.samples[1:] {
		if dist(s) < dist(a) {
			a = s
		}
	}
	var b *sample
	for i := range res.samples {
		s := res.samples[i]
		if s.q == a.q || s.n == a.n { // aom maps neighbouring q to one quantizer
			continue
		}
		if b == nil || dist(s) < dist(*b) {
			b = &res.samples[i]
		}
	}
	if b != nil {
		if s, ok := logSlope(a, *b); ok {
			slope = s
		}
	}
	return modelQ(a, slope, target)
}

// logSlope is d log(bytes) / d q between two samples; ok is false when the
// samples are not increasing (encoder noise), so the caller falls back.
func logSlope(a, b sample) (float64, bool) {
	if a.q == b.q || a.n <= 0 || b.n <= 0 {
		return 0, false
	}
	s := (math.Log(float64(b.n)) - math.Log(float64(a.n))) / float64(b.q-a.q)
	return s, s > 1e-4
}

// modelQ is the highest q at which the line through s with the given slope
// stays at or under target bytes.
func modelQ(s sample, slope, target float64) (int, bool) {
	if slope <= 0 || s.n <= 0 || target <= 0 {
		return 0, false
	}
	return s.q + int(math.Floor((math.Log(target)-math.Log(float64(s.n)))/slope)), true
}

// curveSlope estimates the log-size slope from a side's samples, falling
// back to def.
func curveSlope(samples []sample, def float64) float64 {
	if len(samples) < 2 {
		return def
	}
	lo, hi := samples[0], samples[0]
	for _, s := range samples[1:] {
		if s.q < lo.q {
			lo = s
		}
		if s.q > hi.q {
			hi = s
		}
	}
	if s, ok := logSlope(lo, hi); ok {
		return s
	}
	return def
}

// nearestSample returns the sample whose q is closest to q.
func nearestSample(samples []sample, q int) sample {
	best := samples[0]
	for _, s := range samples[1:] {
		if abs(s.q-q) < abs(best.q-q) {
			best = s
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
