package image

import (
	"fmt"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// fakeBudgetProc returns a proc whose encoder produces a size model
// bytes = pixels * (q/100) * k, monotonic in both side and quality,
// without calling vips. calls records every (side, q) encode.
func fakeBudgetProc(w, h int, k float64, calls *[][2]int) *proc {
	p := &proc{inter: raster{Path: "inter.v", W: w, H: h}, rasters: map[int]raster{}}
	p.rasterHook = func(side int) (raster, error) {
		long := max(w, h)
		return raster{Path: fmt.Sprintf("r%d.v", side), W: w * side / long, H: h * side / long}, nil
	}
	p.encodeHook = func(_ preset.ImageOutput, r raster, q int) (string, int64, error) {
		*calls = append(*calls, [2]int{max(r.W, r.H), q})
		n := int64(float64(r.W*r.H) * float64(q) / 100 * k)
		return fmt.Sprintf("enc-%d-%d", max(r.W, r.H), q), n, nil
	}
	return p
}

var budgetQuality = preset.Quality{Mode: "budget", MaxBytes: 20480, QMax: 60, QMin: 30, DownscaleStep: 0.85, MinSide: 320}

func TestBudgetFitsAtFullSize(t *testing.T) {
	var calls [][2]int
	// 800x600 at q=60 → 480000*0.6*k; k chosen so q≈50 fits.
	p := fakeBudgetProc(800, 600, 20480.0/(480000*0.5), &calls)
	res, err := p.encodeBudget(preset.ImageOutput{Format: "avif"}, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if res.overBudget || res.bytes > 20480 || res.q != 50 {
		t.Fatalf("got %+v, want q=50 within budget", res)
	}
	if res.iterations != len(calls) || res.iterations > 5 {
		t.Fatalf("iterations=%d calls=%d, want ≤5 and equal", res.iterations, len(calls))
	}
	for _, c := range calls {
		if c[0] != 800 {
			t.Fatalf("unexpected downscale: %v", calls)
		}
	}
}

func TestBudgetDownscales(t *testing.T) {
	var calls [][2]int
	// Even qMin at 800px is 4x over budget; must shrink until it fits.
	p := fakeBudgetProc(800, 600, 20480.0/(480000*0.3)*4, &calls)
	res, err := p.encodeBudget(preset.ImageOutput{Format: "webp"}, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if res.overBudget || res.bytes > 20480 {
		t.Fatalf("got %+v, want a fitting result", res)
	}
	side := calls[len(calls)-1][0]
	if side >= 800 || side < 320 {
		t.Fatalf("final side %d, want downscaled within [320,800)", side)
	}
	// Downscaled sides that do not fit cost exactly one encode (at qMin).
	perSide := map[int]int{}
	for _, c := range calls {
		perSide[c[0]]++
	}
	for s, n := range perSide {
		if s != 800 && s != side && n != 1 {
			t.Errorf("side %d: %d encodes, want 1 (qMin probe)", s, n)
		}
	}
	if res.iterations != len(calls) {
		t.Errorf("iterations=%d, calls=%d", res.iterations, len(calls))
	}
}

func TestBudgetUnreachable(t *testing.T) {
	var calls [][2]int
	p := fakeBudgetProc(800, 600, 100, &calls) // never fits
	res, err := p.encodeBudget(preset.ImageOutput{Format: "avif"}, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if !res.overBudget || res.q != 30 {
		t.Fatalf("got %+v, want overBudget at qMin", res)
	}
	if last := calls[len(calls)-1]; last[0] != 320 || last[1] != 30 {
		t.Fatalf("last encode %v, want minSide 320 at qMin 30", last)
	}
	if res.path != "enc-320-30" {
		t.Fatalf("path %s, want the minSide/qMin encode (no re-encode)", res.path)
	}
}

func TestBudgetSmallSource(t *testing.T) {
	var calls [][2]int
	// Source smaller than minSide: no downscaling at all.
	p := fakeBudgetProc(200, 150, 100, &calls)
	res, err := p.encodeBudget(preset.ImageOutput{Format: "avif"}, budgetQuality)
	if err != nil {
		t.Fatal(err)
	}
	if !res.overBudget {
		t.Fatalf("got %+v, want overBudget", res)
	}
	for _, c := range calls {
		if c[0] != 200 {
			t.Fatalf("enlarged or downscaled: %v", calls)
		}
	}
}
