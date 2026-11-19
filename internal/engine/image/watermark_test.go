package image

import (
	"bytes"
	"context"
	"encoding/base64"
	goimage "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func makeSolidPNG(w, h int, c color.NRGBA) []byte {
	img := goimage.NewNRGBA(goimage.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestDecodeWatermarkImage(t *testing.T) {
	pngBytes := makeSolidPNG(8, 8, color.NRGBA{R: 255, A: 255})
	if got, fmtName, err := preset.DecodeWatermarkImage(base64.StdEncoding.EncodeToString(pngBytes)); err != nil || fmtName != "png" || !bytes.Equal(got, pngBytes) {
		t.Fatalf("raw base64 PNG: fmt=%q err=%v", fmtName, err)
	}
	if got, fmtName, err := preset.DecodeWatermarkImage("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)); err != nil || fmtName != "png" || !bytes.Equal(got, pngBytes) {
		t.Fatalf("data URI PNG: fmt=%q err=%v", fmtName, err)
	}
	if _, fmtName, err := preset.DecodeWatermarkImage(base64.StdEncoding.EncodeToString([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "))); err != nil || fmtName != "webp" {
		t.Fatalf("WebP: fmt=%q err=%v", fmtName, err)
	}
	if _, fmtName, err := preset.DecodeWatermarkImage(base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0})); err != nil || fmtName != "jpeg" {
		t.Fatalf("JPEG: fmt=%q err=%v", fmtName, err)
	}

	// Rejects SVG, oversized, empty, invalid base64.
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if _, _, err := preset.DecodeWatermarkImage("data:image/svg+xml;base64," + svg); err == nil {
		t.Fatal("accepted SVG data URI")
	}
	if _, _, err := preset.DecodeWatermarkImage(svg); err == nil {
		t.Fatal("accepted raw SVG payload")
	}
	oversized := make([]byte, preset.MaxWatermarkImageBytes+1)
	copy(oversized, "\x89PNG\r\n\x1a\n")
	if _, _, err := preset.DecodeWatermarkImage(base64.StdEncoding.EncodeToString(oversized)); err == nil {
		t.Fatal("accepted oversized payload")
	}
	if _, _, err := preset.DecodeWatermarkImage("not-base64!!!"); err == nil {
		t.Fatal("accepted invalid base64")
	}
}

func TestCompositeWatermarkBandsAndPositions(t *testing.T) {
	overlay := &rgbaPlane{W: 10, H: 10, Pix: make([]uint8, 10*10*4)}
	for i := 0; i < 10*10; i++ {
		overlay.Pix[4*i] = 255 // pure red opaque
		overlay.Pix[4*i+3] = 255
	}

	for _, bands := range []int{1, 2, 3, 4} {
		for _, pos := range []string{"bottom-right", "bottom-left", "top-right", "top-left", "center", "tile"} {
			dst := make([]uint8, 80*60*bands)
			if bands == 2 || bands == 4 {
				for i := range 80 * 60 {
					dst[i*bands+bands-1] = 255
				}
			}
			compositeWatermark(dst, 80, 60, bands, overlay, pos, 0.5, 0.05)
			changed := 0
			for i := 0; i < 80*60; i++ {
				if dst[i*bands] > 0 {
					changed++
				}
			}
			if changed == 0 {
				t.Fatalf("bands=%d pos=%s made no pixel changes", bands, pos)
			}
		}
	}
}

func TestVipsProcessWithWatermarks(t *testing.T) {
	eng, err := NewVipsCLI(VipsConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Skip("libvips not installed")
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "base.png")
	// Dark navy solid image so white text & red logo stand out unambiguously.
	if err := os.WriteFile(srcPath, makeSolidPNG(400, 300, color.NRGBA{R: 16, G: 24, B: 48, A: 255}), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := preset.ImageLimits{MaxInputBytes: 10 << 20, MaxInputPixels: 10_000_000, Accept: []string{"png"}}
	info, err := eng.Probe(context.Background(), srcPath, limits)
	if err != nil {
		t.Fatal(err)
	}

	// Compare un-watermarked vs text-watermarked (including XML special chars and leading dash) across quality modes.
	baseParams := preset.ImageParams{
		Kind:        "image",
		Resize:      preset.Resize{MaxWidth: 400, MaxHeight: 300, Fit: "inside"},
		Outputs:     []preset.ImageOutput{{Name: "main", Format: FormatWebP}},
		Quality:     preset.Quality{Mode: "fixed", Q: 90},
		Placeholder: preset.Placeholder{ThumbHash: true, DominantColor: true},
		Limits:      limits,
	}
	plainOut, err := eng.Process(context.Background(), info, baseParams, filepath.Join(dir, "plain"))
	if err != nil {
		t.Fatal(err)
	}
	plainImg, err := decodeWebPViaVips(eng, plainOut.Renditions[0].Path, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		params preset.ImageParams
	}{
		{
			name: "text-fixed",
			params: func() preset.ImageParams {
				p := baseParams
				p.Watermark = &preset.Watermark{
					Text: "- © A & B <2026> -", Position: "bottom-right", Opacity: 0.9, Scale: 0.4, Color: "#FFFFFF",
				}
				return p
			}(),
		},
		{
			name: "text-ssim-grayscale",
			params: func() preset.ImageParams {
				p := baseParams
				p.Color.Grayscale = true
				p.Quality = preset.Quality{Mode: "ssim", Target: 0.985, QMin: 40, QMax: 85}
				p.Watermark = &preset.Watermark{Text: "CONFIDENTIAL", Position: "tile", Opacity: 0.5, Scale: 0.3}
				return p
			}(),
		},
		{
			name: "logo-budget",
			params: func() preset.ImageParams {
				p := baseParams
				p.Quality = preset.Quality{Mode: "budget", MaxBytes: 15000, QMin: 30, QMax: 90}
				logo := makeSolidPNG(64, 32, color.NRGBA{R: 255, G: 60, B: 60, A: 200})
				p.Watermark = &preset.Watermark{
					Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(logo),
					Position: "center", Opacity: 0.8, Scale: 0.3,
				}
				return p
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outDir := filepath.Join(dir, tc.name)
			out, err := eng.Process(context.Background(), info, tc.params, outDir)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			got, err := decodeWebPViaVips(eng, out.Renditions[0].Path, outDir)
			if err != nil {
				t.Fatal(err)
			}
			diffPixels := 0
			for i := 0; i < min(len(plainImg.Pix), len(got.Pix)); i += 4 {
				dr := int(plainImg.Pix[i]) - int(got.Pix[i])
				dg := int(plainImg.Pix[i+1]) - int(got.Pix[i+1])
				db := int(plainImg.Pix[i+2]) - int(got.Pix[i+2])
				if dr*dr+dg*dg+db*db > 100 {
					diffPixels++
				}
			}
			if diffPixels < 10 {
				t.Fatalf("%s: expected visible watermark pixels, got diffPixels=%d", tc.name, diffPixels)
			}
		})
	}
}

func decodeWebPViaVips(eng *VipsCLI, webpPath, tmp string) (*rgbaPlane, error) {
	pngOut := filepath.Join(tmp, filepath.Base(webpPath)+".png")
	var cpu int64
	if _, err := eng.runIn(context.Background(), &cpu, tmp, false, 1, eng.vips, "pngsave", webpPath, pngOut); err != nil {
		return nil, err
	}
	return decodeFile(pngOut)
}
