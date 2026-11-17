package image

import (
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// sizeModel is a fake encoder size: bytes = pixels · k · exp(slope·(q-60)),
// log-linear in q like real encoders, scaled by effortRatio for encodes at
// an effort above the AVIF search effort.
type sizeModel struct {
	k, slope    float64
	effortRatio float64 // 0 means 0.85
}

func (m sizeModel) bytes(w, h, q, effort int) int64 {
	n := float64(w*h) * m.k * math.Exp(m.slope*float64(q-60))
	if effort > avifSearchEffort {
		r := m.effortRatio
		if r == 0 {
			r = 0.85
		}
		n *= r
	}
	return int64(n)
}

type call struct{ side, q, effort int }

// fakeBudgetProc returns a proc whose encoder follows m without calling
// vips. calls records every encode.
func fakeBudgetProc(w, h int, m sizeModel, calls *[]call) *proc {
	p := newProc(nil, nil, "")
	p.inter = raster{Path: "inter.v", W: w, H: h}
	p.rasterHook = func(side int) (raster, error) {
		long := max(w, h)
		return raster{Path: fmt.Sprintf("r%d.v", side), W: w * side / long, H: h * side / long}, nil
	}
	var mu sync.Mutex
	p.encodeHook = func(_ preset.ImageOutput, r raster, q, effort int) (string, int64, error) {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, call{max(r.W, r.H), q, effort})
		return fmt.Sprintf("enc-%d-%d-%d", max(r.W, r.H), q, effort), m.bytes(r.W, r.H, q, effort), nil
	}
	return p
}

var budgetQuality = preset.Quality{Mode: "budget", MaxBytes: 20480, QMax: 60, QMin: 30, DownscaleStep: 0.85, MinSide: 320}

var (
	outWebP = preset.ImageOutput{Format: "webp"}
	outAVIF = preset.ImageOutput{Format: "avif", Effort: 4}
)

// kFor returns the model constant at which quality q exactly fills the
// budget at w×h.
func kFor(w, h, q int, slope float64) float64 {
	return 20480 / (float64(w*h) * math.Exp(slope*float64(q-60)))
}

func TestBudgetFitsAtQMaxInOneEncode(t *testing.T) {
	var calls []call
	p := fakeBudgetProc(800, 600, sizeModel{k: kFor(800, 600, 70, 0.02), slope: 0.02}, &calls)
	res, err := p.encodeBudget(outWebP, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if res.q != 60 || res.iterations != 1 || len(calls) != 1 {
		t.Fatalf("got %+v after %v, want q=60 in one encode", res, calls)
	}
}

func TestBudgetFitsAtFullSize(t *testing.T) {
	var calls []call
	m := sizeModel{k: kFor(800, 600, 50, 0.02), slope: 0.02}
	p := fakeBudgetProc(800, 600, m, &calls)
	res, err := p.encodeBudget(outWebP, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if res.overBudget || res.bytes > 20480 || res.q > 50 || res.q < 50-(searchTol-1) {
		t.Fatalf("got %+v, want q in [%d,50] within budget", res, 50-(searchTol-1))
	}
	if res.iterations != len(calls) || res.iterations > 4 {
		t.Fatalf("iterations=%d calls=%v, want ≤4 and equal", res.iterations, calls)
	}
	for _, c := range calls {
		if c.side != 800 {
			t.Fatalf("unexpected downscale: %v", calls)
		}
	}
}

func TestBudgetAVIFSearchesCheapThenFinal(t *testing.T) {
	var calls []call
	// At search effort q=50 fills the budget; final effort is 15 % smaller.
	m := sizeModel{k: kFor(800, 600, 50, 0.045), slope: 0.045}
	p := fakeBudgetProc(800, 600, m, &calls)
	res, err := p.encodeBudget(outAVIF, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if res.overBudget || res.bytes > 20480 {
		t.Fatalf("got %+v, want within budget", res)
	}
	if res.effort != 4 {
		t.Fatalf("delivered effort %d, want the preset effort 4", res.effort)
	}
	if res.q <= 50 {
		t.Fatalf("final q=%d, want > 50 (final effort compresses better)", res.q)
	}
	finals := 0
	for _, c := range calls {
		if c.effort == 4 {
			finals++
		}
	}
	if finals < 1 || finals > 2 || res.iterations > 6 {
		t.Fatalf("calls %v: want 1–2 final-effort encodes and ≤6 total", calls)
	}
}

func TestBudgetAVIFFinalLargerFallsBack(t *testing.T) {
	var calls []call
	// Pure-noise case: the final effort is larger than the search effort.
	m := sizeModel{k: kFor(800, 600, 50, 0.045), slope: 0.045, effortRatio: 1.1}
	p := fakeBudgetProc(800, 600, m, &calls)
	res, err := p.encodeBudget(outAVIF, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if res.overBudget || res.bytes > 20480 {
		t.Fatalf("got %+v, want the fitting search-effort encode", res)
	}
	if res.effort != avifSearchEffort {
		t.Fatalf("got effort %d, want fallback to the search effort", res.effort)
	}
}

func TestBudgetDownscales(t *testing.T) {
	for _, out := range []preset.ImageOutput{outWebP, outAVIF} {
		t.Run(out.Format, func(t *testing.T) {
			var calls []call
			// Even qMin at 800 px is 4× over budget; must shrink until it fits.
			slope := 0.02
			m := sizeModel{k: kFor(800, 600, 30, slope) * 4, slope: slope}
			p := fakeBudgetProc(800, 600, m, &calls)
			res, err := p.encodeBudget(out, budgetQuality)
			if err != nil {
				t.Fatal(err)
			}
			if res.overBudget || res.bytes > 20480 {
				t.Fatalf("got %+v, want a fitting result", res)
			}
			side := calls[len(calls)-1].side
			if side >= 800 || side < 320 {
				t.Fatalf("final side %d, want downscaled within [320,800)", side)
			}
			if res.iterations != len(calls) || res.iterations > 10 {
				t.Errorf("iterations=%d, calls=%v", res.iterations, calls)
			}
		})
	}
}

func TestBudgetUnreachable(t *testing.T) {
	var calls []call
	p := fakeBudgetProc(800, 600, sizeModel{k: 100, slope: 0.02}, &calls) // never fits
	res, err := p.encodeBudget(outWebP, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if !res.overBudget || res.q != 30 {
		t.Fatalf("got %+v, want overBudget at qMin", res)
	}
	if last := calls[len(calls)-1]; last.side != 320 || last.q != 30 {
		t.Fatalf("last encode %v, want minSide 320 at qMin 30", last)
	}
	if res.path != "enc-320-30-0" {
		t.Fatalf("path %s, want the minSide/qMin encode (no re-encode)", res.path)
	}
	// Hopeless rungs are skipped: 800 → 320 directly.
	if len(calls) != 3 {
		t.Fatalf("calls %v, want 3 (qMax and qMin at 800, then qMin at 320)", calls)
	}
}

func TestBudgetUnreachableAVIFUsesSmallerFinal(t *testing.T) {
	var calls []call
	p := fakeBudgetProc(800, 600, sizeModel{k: 100, slope: 0.045}, &calls)
	res, err := p.encodeBudget(outAVIF, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if !res.overBudget || res.q != 30 || res.effort != 4 {
		t.Fatalf("got %+v, want overBudget at qMin with the (smaller) final effort", res)
	}
}

func TestBudgetSmallSource(t *testing.T) {
	var calls []call
	// Source smaller than minSide: no downscaling at all.
	p := fakeBudgetProc(200, 150, sizeModel{k: 100, slope: 0.02}, &calls)
	res, err := p.encodeBudget(outWebP, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if !res.overBudget {
		t.Fatalf("got %+v, want overBudget", res)
	}
	for _, c := range calls {
		if c.side != 200 {
			t.Fatalf("enlarged or downscaled: %v", calls)
		}
	}
}

// TestBudgetEncodeCountBounded sweeps image "difficulty" across four orders
// of magnitude and asserts the budget guarantee and an upper bound on the
// number of encodes per output (the cost driver of the CLI engine).
func TestBudgetEncodeCountBounded(t *testing.T) {
	const (
		maxFullSize = 6  // fits at full size (incl. up to 2 AVIF final encodes)
		maxTotal    = 12 // with downscaling
	)
	for _, out := range []preset.ImageOutput{outWebP, outAVIF} {
		for _, slope := range []float64{0.01, 0.02, 0.045, 0.07} {
			for f := 0.05; f < 500; f *= 1.37 {
				var calls []call
				m := sizeModel{k: kFor(800, 600, 60, slope) * f, slope: slope}
				p := fakeBudgetProc(800, 600, m, &calls)
				res, err := p.encodeBudget(out, budgetQuality)
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("%s slope=%.3f f=%.2f", out.Format, slope, f)
				if res.bytes > 20480 && !res.overBudget {
					t.Errorf("%s: %d bytes over budget without OverBudget", name, res.bytes)
				}
				if res.iterations != len(calls) {
					t.Errorf("%s: iterations %d != calls %d", name, res.iterations, len(calls))
				}
				downscaled := false
				for _, c := range calls {
					downscaled = downscaled || c.side != 800
				}
				limit := maxFullSize
				if downscaled {
					limit = maxTotal
				}
				if res.iterations > limit {
					t.Errorf("%s: %d encodes, want ≤ %d: %v", name, res.iterations, limit, calls)
				}
				// Quality: at the delivered side, a fitting single-stage result
				// is within searchTol of the best fitting q.
				if out.Format == FormatWebP && !res.overBudget {
					side := calls[len(calls)-1].side
					for _, c := range calls {
						if c.q == res.q && c.effort == res.effort {
							side = c.side
						}
					}
					w, h := 800*side/800, 600*side/800
					bestQ := 30
					for q := 30; q <= 60; q++ {
						if m.bytes(w, h, q, 0) <= 20480 {
							bestQ = q
						}
					}
					if res.q < bestQ-(searchTol-1) && float64(res.bytes) < (1-searchByteSlack)*20480 {
						t.Errorf("%s: q=%d (%d B) but q=%d fits at side %d", name, res.q, res.bytes, bestQ, side)
					}
				}
			}
		}
	}
}

func TestSearchQBisectsOnNoise(t *testing.T) {
	// A non-monotone size curve must still terminate within the bound.
	sizes := map[int]int64{}
	for q := 1; q <= 100; q++ {
		sizes[q] = int64(10000 + (q%7)*3000 + q*50)
	}
	n := 0
	res, err := searchQ(30, 60, 60, 14000, slopeWebP, func(q int) (int64, error) { n++; return sizes[q], nil })
	if err != nil {
		t.Fatal(err)
	}
	if n > maxSearchProbes {
		t.Fatalf("%d probes, want ≤ %d", n, maxSearchProbes)
	}
	if res.found(30) && sizes[res.fit] > 14000 {
		t.Fatalf("fit q=%d is %d bytes", res.fit, sizes[res.fit])
	}
}
