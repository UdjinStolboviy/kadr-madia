package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func TestSSIMOfRendition(t *testing.T) {
	e, err := NewVipsCLI(VipsConfig{})
	if err != nil {
		t.Skip("libvips unavailable")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	if out, err := runCmd(e.vips, "gaussnoise", filepath.Join(dir, "n.v"), "800", "600", "--mean", "128", "--sigma", "30"); err != nil {
		t.Skipf("gaussnoise: %v %s", err, out)
	}
	if out, err := runCmd(e.vips, "pngsave", filepath.Join(dir, "n.v"), src); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var p preset.ImageParams
	p.Kind = "image"
	p.Resize = preset.Resize{MaxWidth: 400, MaxHeight: 400, Fit: "inside"}
	p.Outputs = []preset.ImageOutput{{Name: "main", Format: "webp"}}
	p.Quality = preset.Quality{Mode: "fixed", Q: 80}
	p.Limits = preset.ImageLimits{MaxInputBytes: 50 << 20, MaxInputPixels: 40_000_000, Accept: []string{"png"}}
	info, err := e.Probe(context.Background(), src, p.Limits)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Process(context.Background(), info, p, dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.SSIM(context.Background(), src, out.Renditions[0].Path, "webp")
	if err != nil || s <= 0 || s > 1 {
		t.Fatalf("ssim %v %v", s, err)
	}
	_ = os.Remove(src)
}

func runCmd(bin string, args ...string) (string, error) {
	out, err := execCommand(bin, args...).CombinedOutput()
	return string(out), err
}
