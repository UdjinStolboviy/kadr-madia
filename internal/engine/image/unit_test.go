package image

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestSniffFormat(t *testing.T) {
	ftyp := func(major string, compat ...string) []byte {
		b := []byte{0, 0, 0, 0, 'f', 't', 'y', 'p'}
		b = append(b, major...)
		b = append(b, 0, 0, 0, 0)
		for _, c := range compat {
			b = append(b, c...)
		}
		n := len(b)
		b[0], b[1], b[2], b[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
		return append(b, "\x00\x00\x00\x08meta"...)
	}
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10}, FormatJPEG},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00"), FormatPNG},
		{"gif87", []byte("GIF87a.."), FormatGIF},
		{"gif89", []byte("GIF89a.."), FormatGIF},
		{"webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), FormatWebP},
		{"tiff-le", []byte("II*\x00\x08\x00"), FormatTIFF},
		{"tiff-be", []byte("MM\x00*\x00\x08"), FormatTIFF},
		{"heic", ftyp("heic", "mif1", "heic"), FormatHEIC},
		{"heix", ftyp("heix", "mif1"), FormatHEIC},
		{"avif", ftyp("avif", "mif1", "miaf"), FormatAVIF},
		{"avis", ftyp("avis", "msf1"), FormatAVIF},
		{"mif1+avif", ftyp("mif1", "avif", "miaf"), FormatAVIF},
		{"mif1+heic", ftyp("mif1", "heic"), FormatHEIC},
		{"mp4", ftyp("isom", "iso2", "mp41"), ""},
		{"text", []byte("hello, world"), ""},
		{"empty", nil, ""},
		{"riff-wav", []byte("RIFF\x10\x00\x00\x00WAVEfmt "), ""},
	}
	for _, c := range cases {
		if got := sniffFormat(c.head); got != c.want {
			t.Errorf("%s: sniffFormat = %q, want %q", c.name, got, c.want)
		}
	}
}

func jpegStream(withThumb bool, truncate bool) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	if withThumb {
		// APP1 that contains an embedded SOI..EOI thumbnail.
		payload := []byte("Exif\x00\x00\xFF\xD8\xFF\xDB\x00\x02\xFF\xD9")
		n := len(payload) + 2
		b.Write([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)})
		b.Write(payload)
	}
	b.Write([]byte{0xFF, 0xDB, 0x00, 0x04, 0x00, 0x00}) // DQT
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x03, 0x00})       // SOS
	b.Write([]byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD0, 0x56, 0x78})
	if !truncate {
		b.Write([]byte{0xFF, 0xD9})
	}
	return b.Bytes()
}

func TestCheckJPEG(t *testing.T) {
	for _, c := range []struct {
		name          string
		thumb, trunc  bool
		wantTruncated bool
	}{
		{"complete", false, false, false},
		{"complete-with-thumb", true, false, false},
		{"truncated", false, true, true},
		{"truncated-with-thumb", true, true, true},
	} {
		err := checkJPEG(bufio.NewReader(bytes.NewReader(jpegStream(c.thumb, c.trunc))))
		if (err != nil) != c.wantTruncated {
			t.Errorf("%s: err = %v, want truncated=%v", c.name, err, c.wantTruncated)
		}
	}
}

func TestCheckPNG(t *testing.T) {
	chunk := func(typ string, data []byte) []byte {
		n := len(data)
		b := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
		b = append(b, typ...)
		b = append(b, data...)
		return append(b, 0, 0, 0, 0)
	}
	full := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", make([]byte, 13))...)
	full = append(full, chunk("IDAT", make([]byte, 100))...)
	full = append(full, chunk("IEND", nil)...)
	if err := checkPNG(bufio.NewReader(bytes.NewReader(full))); err != nil {
		t.Fatalf("complete png: %v", err)
	}
	if err := checkPNG(bufio.NewReader(bytes.NewReader(full[:60]))); err == nil {
		t.Fatal("truncated png: want error")
	}
}

func testRGBA(w, h int, alpha bool) []byte {
	p := make([]byte, w*h*4)
	for y := range h {
		for x := range w {
			i := (y*w + x) * 4
			p[i] = byte(x * 255 / max(w-1, 1))
			p[i+1] = byte(y * 255 / max(h-1, 1))
			p[i+2] = byte((x*7 + y*13) % 256)
			p[i+3] = 255
			if alpha {
				p[i+3] = byte(((x + y) % 5) * 60)
			}
		}
	}
	return p
}

// thumbHashClose compares hashes allowing each AC nibble to differ by 1.
// AC terms that are mathematically zero normalise to exactly 7.5 and then
// round up or down depending on the last-ulp behaviour of cos(), which
// differs between Go's math.Cos and JavaScript engines. Header bytes (DC
// terms, scales, flags) must match exactly.
func thumbHashClose(a, b []byte) bool {
	if len(a) != len(b) || len(a) < 5 {
		return false
	}
	acStart := 5
	if a[2]&0x80 != 0 { // hasAlpha
		acStart = 6
	}
	if !bytes.Equal(a[:acStart], b[:acStart]) {
		return false
	}
	for i := acStart; i < len(a); i++ {
		for _, sh := range []uint{0, 4} {
			d := int(a[i]>>sh&15) - int(b[i]>>sh&15)
			if d < -1 || d > 1 {
				return false
			}
		}
	}
	return true
}

// Vectors produced by the reference JavaScript implementation
// (rgbaToThumbHash from github.com/evanw/thumbhash) on testRGBA images.
func TestThumbHashReferenceVectors(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		alpha bool
		want  string
	}{
		{32, 24, false, "3/cJPZpwh3eBdndYh2eXiINwB/h3"},
		{20, 30, true, "HwiGGwwHcIGHRoeCcAf4eHh4Fwd3iHA="},
		{100, 75, false, "3wcKNZqAh3dwd3d4eHh3eHCAB/h3"},
		{1, 1, false, "AAgCBwAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		got, err := rgbaToThumbHash(c.w, c.h, testRGBA(c.w, c.h, c.alpha))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := base64.StdEncoding.DecodeString(c.want)
		if !thumbHashClose(got, want) {
			t.Errorf("%dx%d alpha=%v: got %s, want %s", c.w, c.h, c.alpha, base64.StdEncoding.EncodeToString(got), c.want)
		}
	}
	if _, err := rgbaToThumbHash(101, 10, make([]byte, 101*10*4)); err == nil {
		t.Error("101px wide: want error")
	}
}

func TestThumbHashAverageColor(t *testing.T) {
	w, h := 16, 16
	p := make([]byte, w*h*4)
	for i := 0; i < len(p); i += 4 {
		p[i], p[i+1], p[i+2], p[i+3] = 200, 100, 50, 255
	}
	hash, err := rgbaToThumbHash(w, h, p)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := thumbHashAverageRGBA(hash)
	for _, d := range []struct{ got, want float64 }{{r, 200}, {g, 100}, {b, 50}, {a * 255, 255}} {
		if math.Abs(d.got*255-d.want) > 8 && math.Abs(d.got-d.want) > 8 {
			t.Errorf("average color %v %v %v %v, want ~200,100,50,255", r*255, g*255, b*255, a*255)
			break
		}
	}
	if got := dominantColor(&rgbaPlane{W: w, H: h, Pix: p}); got != "#c86432" {
		t.Errorf("dominantColor = %s, want #c86432", got)
	}
}

func TestSSIM(t *testing.T) {
	w, h := 64, 48
	a := &grayPlane{W: w, H: h, Pix: make([]float64, w*h)}
	for i := range a.Pix {
		a.Pix[i] = float64((i*37)%251) * 0.9
	}
	if s := ssim(a, a); math.Abs(s-1) > 1e-9 {
		t.Fatalf("identical: ssim = %v", s)
	}
	b := &grayPlane{W: w, H: h, Pix: make([]float64, w*h)}
	for i := range b.Pix {
		b.Pix[i] = a.Pix[i] + float64((i*13)%21-10) // mild noise
	}
	c := &grayPlane{W: w, H: h, Pix: make([]float64, w*h)}
	for i := range c.Pix {
		c.Pix[i] = 128 // structure destroyed
	}
	sb, sc := ssim(a, b), ssim(a, c)
	if !(sb < 1 && sb > sc && sc < 0.2) {
		t.Fatalf("ssim ordering wrong: noisy=%v flat=%v", sb, sc)
	}
	if d := a.boxDown(2); d.W != 32 || d.H != 24 {
		t.Fatalf("boxDown size %dx%d", d.W, d.H)
	}
	if r := a.resample(30, 20); r.W != 30 || r.H != 20 {
		t.Fatalf("resample size %dx%d", r.W, r.H)
	}
}

func TestCropBoxAndAspect(t *testing.T) {
	if w, h := cropBox(256, 256, 4000, 3000, 1); w != 256 || h != 256 {
		t.Errorf("large source: %dx%d", w, h)
	}
	if w, h := cropBox(256, 256, 200, 150, 1); w != 150 || h != 150 {
		t.Errorf("small source must not enlarge: %dx%d", w, h)
	}
	if w, h := cropBox(800, 800, 4000, 3000, 16.0/9); w != 800 || h != 450 {
		t.Errorf("16:9: %dx%d", w, h)
	}
	if a, err := parseAspect("4:3"); err != nil || math.Abs(a-4.0/3) > 1e-9 {
		t.Errorf("parseAspect 4:3 = %v, %v", a, err)
	}
	for _, bad := range []string{"", "1", "0:1", "a:b", "1:-2"} {
		if _, err := parseAspect(bad); err == nil {
			t.Errorf("parseAspect(%q): want error", bad)
		}
	}
}

func TestParseHeader(t *testing.T) {
	h := parseHeader("width: 4032\nheight: 3024\nbands: 3\nn-pages: 1\norientation: 6\nvips-loader: jpegload\nexif-ifd0-Orientation: 6 (Rotate 90 CW, Short, 1 components, 2 bytes)\n")
	if h.Width != 4032 || h.Height != 3024 || h.Bands != 3 || h.Orientation != 6 || h.Loader != "jpegload" || h.Pages != 1 {
		t.Fatalf("parseHeader = %+v", h)
	}
}

func TestReadVHeaderRejectsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.v")
	if err := os.WriteFile(p, []byte("not a vips file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readVHeader(p); err == nil {
		t.Fatal("want error")
	}
	le := []byte{0xb6, 0xa6, 0xf2, 0x08, 0x20, 0x03, 0, 0, 0x58, 0x02, 0, 0, 3, 0, 0, 0}
	if err := os.WriteFile(p, le, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := readVHeader(p)
	if err != nil || r.W != 800 || r.H != 600 || r.Bands != 3 {
		t.Fatalf("got %+v, %v", r, err)
	}
}
