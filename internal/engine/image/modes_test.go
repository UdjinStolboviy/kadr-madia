package image

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func modeParams(t *testing.T, mode string) preset.ImageParams {
	for _, d := range preset.ModePresets() {
		if d.Slug == preset.ModeSlug("image", mode) {
			var p preset.ImageParams
			if err := json.Unmarshal(d.Params, &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	t.Fatalf("no preset for mode %s", mode)
	return preset.ImageParams{}
}

// The three compression modes must be ordered: compact is the smallest,
// quality the largest and sharpest.
func TestCompressionModesAreOrdered(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: encodes three real photos")
	}
	e, err := NewVipsCLI(VipsConfig{Timeout: 120 * time.Second})
	if err != nil {
		t.Skip("libvips unavailable")
	}
	src, err := filepath.Abs(filepath.Join(fixturesDir, "photo.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	type res struct {
		bytes int64
		w     int
		ssim  float64
	}
	got := map[string]res{}
	for _, mode := range preset.Modes {
		p := modeParams(t, mode)
		info, err := e.Probe(context.Background(), src, p.Limits)
		if err != nil {
			t.Skipf("fixture not usable: %v", err)
		}
		out, err := e.Process(context.Background(), info, p, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		var avif *Rendition
		for i := range out.Renditions {
			if out.Renditions[i].Format == "avif" {
				avif = &out.Renditions[i]
			}
		}
		if avif == nil {
			t.Fatalf("%s: no avif", mode)
		}
		s, err := e.SSIM(context.Background(), src, avif.Path, "avif")
		if err != nil {
			t.Fatal(err)
		}
		got[mode] = res{avif.Bytes, avif.Width, s}
		t.Logf("%-8s %7d bytes  %4dpx  ssim %.4f  q%d", mode, avif.Bytes, avif.Width, s, avif.Quality)
	}
	c, b, q := got[preset.ModeCompact], got[preset.ModeBalanced], got[preset.ModeQuality]
	if !(c.bytes < b.bytes && b.bytes < q.bytes) {
		t.Errorf("sizes not ordered: compact %d, balanced %d, quality %d", c.bytes, b.bytes, q.bytes)
	}
	if !(c.ssim < q.ssim) {
		t.Errorf("quality must beat compact: %.4f vs %.4f", q.ssim, c.ssim)
	}
}
