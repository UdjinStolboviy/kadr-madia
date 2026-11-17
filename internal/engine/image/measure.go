package image

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// SSIM returns the structural similarity (0–1) between the source image and
// an encoded rendition. The source is resized to the rendition's size first,
// so the score reflects compression and resizing artifacts, not the geometry
// change. Used by the admin Playground.
func (e *VipsCLI) SSIM(ctx context.Context, srcPath, renditionPath, format string) (float64, error) {
	if err := e.acquire(ctx); err != nil {
		return 0, err
	}
	defer e.release()
	dir, err := os.MkdirTemp("", "kadr-ssim-")
	if err != nil {
		return 0, engineErr("ssim temp dir", err)
	}
	defer os.RemoveAll(dir)

	var cpu int64
	rendPNG := filepath.Join(dir, "r.png")
	if _, err := e.runIn(ctx, &cpu, dir, false, 1, e.vips, "pngsave", renditionPath, rendPNG, "--compression", "1"); err != nil {
		return 0, engineErr("decode rendition", err)
	}
	rend, err := decodeFile(rendPNG)
	if err != nil {
		return 0, engineErr("decode rendition", err)
	}
	srcPNG := filepath.Join(dir, "s.png")
	if _, err := e.runIn(ctx, &cpu, dir, true, 1, e.vips, "thumbnail", srcPath, srcPNG, strconv.Itoa(rend.W),
		"--height", strconv.Itoa(rend.H), "--size", "force"); err != nil {
		return 0, engineErr("resize source", err)
	}
	src, err := decodeFile(srcPNG)
	if err != nil {
		return 0, engineErr("decode source", err)
	}
	a, b := src.luma(), rend.luma()
	f := ssimFactor(a.W, a.H)
	return ssim(a.boxDown(f), b.boxDown(f)), nil
}
