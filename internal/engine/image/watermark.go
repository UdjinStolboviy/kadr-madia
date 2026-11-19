package image

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// applyWatermark composites a preset's static text or image watermark onto the
// uncoded 8-bit .v intermediate at cur and returns the path of the new .v file.
func (p *proc) applyWatermark(cur string, wm *preset.Watermark) (string, error) {
	if wm == nil || (strings.TrimSpace(wm.Text) == "" && strings.TrimSpace(wm.Image) == "") {
		return cur, nil
	}
	raw, err := os.ReadFile(cur)
	if err != nil {
		return "", engineErr("watermark: read raster", err)
	}
	if len(raw) < vHeaderLen {
		return "", engineErr("watermark: short .v file", fmt.Errorf("len %d < %d", len(raw), vHeaderLen))
	}
	h, err := parseVHeader(raw[:vHeaderLen])
	if err != nil {
		return "", engineErr("watermark: parse header", err)
	}
	if h.Format != vFormatUchar || h.Coding != vCodingNone || h.Bands < 1 || h.Bands > 4 {
		return "", engineErr("watermark: unsupported raster format", errVUnsupported)
	}
	pixBytes := h.W * h.H * h.Bands
	if len(raw) < vHeaderLen+pixBytes {
		return "", engineErr("watermark: truncated pixel buffer", fmt.Errorf("len %d < %d", len(raw), vHeaderLen+pixBytes))
	}

	opacity := wm.Opacity
	if opacity <= 0 {
		opacity = 0.35
	}
	opacity = min(1.0, opacity)

	scale := wm.Scale
	if scale <= 0 {
		scale = 0.25
	}
	scale = max(0.05, min(1.0, scale))

	margin := wm.Margin
	if margin <= 0 {
		margin = 0.04
	}
	margin = max(0, min(0.25, margin))

	pos := strings.TrimSpace(wm.Position)
	if pos == "" {
		pos = "bottom-right"
	}

	var overlay *rgbaPlane
	if text := strings.TrimSpace(wm.Text); text != "" {
		overlay, err = p.renderTextWatermark(text, wm.Font, wm.Color, h.W, h.H, scale)
	} else {
		overlay, err = p.renderImageWatermark(wm.Image, h.W, h.H, scale)
	}
	if err != nil {
		return "", err
	}

	pix := raw[vHeaderLen : vHeaderLen+pixBytes]
	compositeWatermark(pix, h.W, h.H, h.Bands, overlay, pos, opacity, margin)

	next := p.tmpFile(".v")
	if err := os.WriteFile(next, raw, 0o600); err != nil {
		return "", engineErr("watermark: write raster", err)
	}
	return next, nil
}

func (p *proc) renderTextWatermark(text, font, hexColor string, baseW, baseH int, scale float64) (*rgbaPlane, error) {
	if strings.TrimSpace(font) == "" {
		font = "Sans Bold"
	}
	targetW := max(24, int(math.Round(float64(baseW)*scale)))
	targetH := max(16, baseH/2)

	var esc bytes.Buffer
	if err := xml.EscapeText(&esc, []byte(text)); err != nil {
		return nil, engineErr("watermark: escape text", err)
	}
	maskPath := p.tmpFile(".v")
	// "--" keeps text starting with "-" from being parsed as a CLI option.
	if _, err := p.vips("text",
		"--width", strconv.Itoa(targetW),
		"--height", strconv.Itoa(targetH),
		"--font", font,
		"--align", "centre",
		"--", maskPath, esc.String()); err != nil {
		return nil, engineErr("watermark: render text", err)
	}
	mask, err := readVImage(maskPath)
	if err != nil {
		return nil, engineErr("watermark: read text mask", err)
	}
	r, g, b := parseHexColor(hexColor)
	return colorizeTextMask(mask, r, g, b), nil
}

func (p *proc) renderImageWatermark(encoded string, baseW, baseH int, scale float64) (*rgbaPlane, error) {
	rawBytes, _, err := preset.DecodeWatermarkImage(encoded)
	if err != nil {
		return nil, &Error{Code: CodeEngineFailure, Detail: "invalid watermark image: " + err.Error()}
	}
	inPath := p.tmpFile(".wm")
	if err := os.WriteFile(inPath, rawBytes, 0o600); err != nil {
		return nil, engineErr("watermark: write logo", err)
	}
	targetW := max(16, int(math.Round(float64(baseW)*scale)))
	targetH := max(16, baseH)
	outPath := p.tmpFile(".v")
	if _, err := p.vipsSource("thumbnail", inPath, outPath, strconv.Itoa(targetW),
		"--height", strconv.Itoa(targetH), "--size", "both", "--export-profile", "srgb"); err != nil {
		return nil, engineErr("watermark: decode logo", err)
	}
	h, err := readVFileHeader(outPath)
	if err != nil {
		return nil, engineErr("watermark: read logo header", err)
	}
	if h.Format != vFormatUchar || h.Coding != vCodingNone || h.Bands > 4 {
		conv := p.tmpFile(".v")
		if _, err := p.vips("colourspace", outPath, conv, "srgb"); err != nil {
			return nil, engineErr("watermark: convert logo to sRGB", err)
		}
		outPath = conv
	}
	img, err := readVImage(outPath)
	if err != nil {
		return nil, engineErr("watermark: read logo pixels", err)
	}
	return img, nil
}

func parseHexColor(hex string) (uint8, uint8, uint8) {
	s := strings.TrimSpace(hex)
	if len(s) != 7 || s[0] != '#' {
		return 255, 255, 255
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 255, 255, 255
	}
	return uint8((v >> 16) & 0xff), uint8((v >> 8) & 0xff), uint8(v & 0xff)
}

// colorizeTextMask creates an RGBA overlay from a 1-band coverage mask with a
// subtle dark drop-shadow so white or colored text stays legible over any background.
func colorizeTextMask(mask *rgbaPlane, cr, cg, cb uint8) *rgbaPlane {
	sh := 1
	if mask.H >= 48 {
		sh = 2
	}
	w, h := mask.W+sh, mask.H+sh
	out := &rgbaPlane{W: w, H: h, Pix: make([]uint8, w*h*4)}

	// Shadow pass offset by (+sh, +sh).
	for y := range mask.H {
		for x := range mask.W {
			cov := mask.Pix[(y*mask.W+x)*4]
			if cov == 0 {
				continue
			}
			sa := uint8(math.Round(float64(cov) * 0.45))
			di := ((y+sh)*w + (x + sh)) * 4
			out.Pix[di], out.Pix[di+1], out.Pix[di+2], out.Pix[di+3] = 0, 0, 0, sa
		}
	}

	// Foreground text composited over shadow (non-premultiplied source-over).
	for y := range mask.H {
		for x := range mask.W {
			cov := mask.Pix[(y*mask.W+x)*4]
			if cov == 0 {
				continue
			}
			sa := float64(cov) / 255.0
			di := (y*w + x) * 4
			da := float64(out.Pix[di+3]) / 255.0
			outA := sa + da*(1-sa)
			if outA <= 0 {
				continue
			}
			out.Pix[di] = clampU8((float64(cr)*sa + float64(out.Pix[di])*da*(1-sa)) / outA)
			out.Pix[di+1] = clampU8((float64(cg)*sa + float64(out.Pix[di+1])*da*(1-sa)) / outA)
			out.Pix[di+2] = clampU8((float64(cb)*sa + float64(out.Pix[di+2])*da*(1-sa)) / outA)
			out.Pix[di+3] = clampU8(outA * 255.0)
		}
	}
	return out
}

func compositeWatermark(dst []uint8, dw, dh, bands int, overlay *rgbaPlane, pos string, opacity, margin float64) {
	if overlay == nil || overlay.W == 0 || overlay.H == 0 || opacity <= 0 {
		return
	}
	if pos == "tile" {
		rot := rotateRGBA(overlay, -30)
		stepX := rot.W + max(24, dw/8)
		stepY := rot.H + max(24, dh/8)
		for row, y := 0, -rot.H/2; y < dh; row, y = row+1, y+stepY {
			shift := 0
			if row%2 == 1 {
				shift = stepX / 2
			}
			for x := -rot.W/2 + shift; x < dw; x += stepX {
				blendRGBAOntoV(dst, dw, dh, bands, rot, x, y, opacity)
			}
		}
		return
	}
	pad := int(math.Round(float64(min(dw, dh)) * margin))
	var ox, oy int
	switch pos {
	case "top-left":
		ox, oy = pad, pad
	case "top-right":
		ox, oy = dw-overlay.W-pad, pad
	case "bottom-left":
		ox, oy = pad, dh-overlay.H-pad
	case "center":
		ox, oy = (dw-overlay.W)/2, (dh-overlay.H)/2
	default: // bottom-right
		ox, oy = dw-overlay.W-pad, dh-overlay.H-pad
	}
	if ox+overlay.W > dw {
		ox = dw - overlay.W
	}
	if oy+overlay.H > dh {
		oy = dh - overlay.H
	}
	if ox < 0 {
		ox = 0
	}
	if oy < 0 {
		oy = 0
	}
	blendRGBAOntoV(dst, dw, dh, bands, overlay, ox, oy, opacity)
}

func blendRGBAOntoV(dst []uint8, dw, dh, bands int, src *rgbaPlane, ox, oy int, opacity float64) {
	yStart := max(0, -oy)
	yEnd := min(src.H, dh-oy)
	xStart := max(0, -ox)
	xEnd := min(src.W, dw-ox)
	for sy := yStart; sy < yEnd; sy++ {
		dy := oy + sy
		for sx := xStart; sx < xEnd; sx++ {
			si := (sy*src.W + sx) * 4
			aByte := src.Pix[si+3]
			if aByte == 0 {
				continue
			}
			sa := (float64(aByte) / 255.0) * opacity
			if sa <= 0 {
				continue
			}
			inv := 1.0 - sa
			di := (dy*dw + (ox + sx)) * bands
			sr, sg, sb := float64(src.Pix[si]), float64(src.Pix[si+1]), float64(src.Pix[si+2])
			switch bands {
			case 1:
				lum := 0.2126*sr + 0.7152*sg + 0.0722*sb
				dst[di] = clampU8(lum*sa + float64(dst[di])*inv)
			case 2:
				lum := 0.2126*sr + 0.7152*sg + 0.0722*sb
				da := float64(dst[di+1]) / 255.0
				outA := sa + da*inv
				if outA > 0 {
					dst[di] = clampU8((lum*sa + float64(dst[di])*da*inv) / outA)
					dst[di+1] = clampU8(outA * 255.0)
				}
			case 3:
				dst[di] = clampU8(sr*sa + float64(dst[di])*inv)
				dst[di+1] = clampU8(sg*sa + float64(dst[di+1])*inv)
				dst[di+2] = clampU8(sb*sa + float64(dst[di+2])*inv)
			case 4:
				da := float64(dst[di+3]) / 255.0
				outA := sa + da*inv
				if outA > 0 {
					dst[di] = clampU8((sr*sa + float64(dst[di])*da*inv) / outA)
					dst[di+1] = clampU8((sg*sa + float64(dst[di+1])*da*inv) / outA)
					dst[di+2] = clampU8((sb*sa + float64(dst[di+2])*da*inv) / outA)
					dst[di+3] = clampU8(outA * 255.0)
				}
			}
		}
	}
}

// rotateRGBA rotates an RGBA image around its center by angleDeg using
// premultiplied bilinear interpolation to avoid dark fringes on alpha edges.
func rotateRGBA(src *rgbaPlane, angleDeg float64) *rgbaPlane {
	rad := angleDeg * math.Pi / 180.0
	cosA, sinA := math.Cos(rad), math.Sin(rad)
	w, h := float64(src.W), float64(src.H)
	outW := max(1, int(math.Ceil(math.Abs(w*cosA)+math.Abs(h*sinA))))
	outH := max(1, int(math.Ceil(math.Abs(w*sinA)+math.Abs(h*cosA))))
	out := &rgbaPlane{W: outW, H: outH, Pix: make([]uint8, outW*outH*4)}

	scx, scy := (w-1)/2.0, (h-1)/2.0
	dcx, dcy := (float64(outW)-1)/2.0, (float64(outH)-1)/2.0

	sample := func(ix, iy int) (r, g, b, a float64) {
		if ix < 0 || ix >= src.W || iy < 0 || iy >= src.H {
			return 0, 0, 0, 0
		}
		i := (iy*src.W + ix) * 4
		al := float64(src.Pix[i+3]) / 255.0
		return float64(src.Pix[i]) * al, float64(src.Pix[i+1]) * al, float64(src.Pix[i+2]) * al, al
	}

	for dy := range outH {
		ry := float64(dy) - dcy
		for dx := range outW {
			rx := float64(dx) - dcx
			sx := cosA*rx + sinA*ry + scx
			sy := -sinA*rx + cosA*ry + scy
			x0, y0 := int(math.Floor(sx)), int(math.Floor(sy))
			fx, fy := sx-float64(x0), sy-float64(y0)
			if x0+1 < 0 || x0 >= src.W || y0+1 < 0 || y0 >= src.H {
				continue
			}
			r00, g00, b00, a00 := sample(x0, y0)
			r10, g10, b10, a10 := sample(x0+1, y0)
			r01, g01, b01, a01 := sample(x0, y0+1)
			r11, g11, b11, a11 := sample(x0+1, y0+1)

			w00 := (1 - fx) * (1 - fy)
			w10 := fx * (1 - fy)
			w01 := (1 - fx) * fy
			w11 := fx * fy
			a := a00*w00 + a10*w10 + a01*w01 + a11*w11
			if a <= 1e-6 {
				continue
			}
			r := (r00*w00 + r10*w10 + r01*w01 + r11*w11) / a
			g := (g00*w00 + g10*w10 + g01*w01 + g11*w11) / a
			b := (b00*w00 + b10*w10 + b01*w01 + b11*w11) / a
			di := (dy*outW + dx) * 4
			out.Pix[di] = clampU8(r)
			out.Pix[di+1] = clampU8(g)
			out.Pix[di+2] = clampU8(b)
			out.Pix[di+3] = clampU8(a * 255.0)
		}
	}
	return out
}

func clampU8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}
