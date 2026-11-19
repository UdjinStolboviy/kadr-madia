package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/engine/fakeimage"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

type fakePlayground struct{ *fakeimage.Engine }

func (fakePlayground) SSIM(context.Context, string, string, string) (float64, error) {
	return 0.97, nil
}

func TestValidateImageParams(t *testing.T) {
	var p preset.ImageParams
	if validateImageParams(&p) == "" {
		t.Fatal("empty params accepted")
	}
	for _, d := range preset.DefaultPresets() {
		if d.Kind != "image" {
			continue
		}
		var ip preset.ImageParams
		_ = json.Unmarshal(d.Params, &ip)
		if m := validateImageParams(&ip); m != "" {
			t.Errorf("%s rejected: %s", d.Slug, m)
		}
	}

	// Watermark validation cases.
	var valid preset.ImageParams
	_ = json.Unmarshal(preset.DefaultPresets()[0].Params, &valid)
	valid.Watermark = &preset.Watermark{Text: "  Acme  ", Position: "tile"}
	if m := validateImageParams(&valid); m != "" {
		t.Fatalf("valid text watermark rejected: %s", m)
	}
	if valid.Watermark.Text != "Acme" || valid.Watermark.Opacity != 0.35 || valid.Watermark.Scale != 0.25 || valid.Watermark.Color != "#FFFFFF" || valid.Watermark.Font != "Sans Bold" {
		t.Fatalf("watermark defaults not normalized: %+v", valid.Watermark)
	}

	// Empty watermark object clears to nil.
	valid.Watermark = &preset.Watermark{}
	if m := validateImageParams(&valid); m != "" || valid.Watermark != nil {
		t.Fatalf("empty watermark should normalize to nil: %+v msg=%q", valid.Watermark, m)
	}

	// Both text and image rejected.
	png1x1 := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfeA\xe2&\x05\x00\x00\x00\x00IEND\xaeB`\x82"))
	valid.Watermark = &preset.Watermark{Text: "x", Image: png1x1}
	if validateImageParams(&valid) == "" {
		t.Fatal("accepted watermark with both text and image")
	}
	// SVG rejected.
	valid.Watermark = &preset.Watermark{Image: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))}
	if validateImageParams(&valid) == "" {
		t.Fatal("accepted SVG watermark")
	}
	// Bad color, font, position.
	for _, bad := range []*preset.Watermark{
		{Text: "ok", Color: "red"},
		{Text: "ok", Font: "-badflag"},
		{Text: "ok", Position: "diagonal"},
		{Text: "ok", Opacity: 1.5},
		{Text: "ok", Scale: 0.01},
		{Text: "ok", Margin: 0.5},
	} {
		valid.Watermark = bad
		if validateImageParams(&valid) == "" {
			t.Fatalf("accepted invalid watermark: %+v", bad)
		}
	}
	// Valid PNG image watermark accepted.
	valid.Watermark = &preset.Watermark{Image: png1x1, Position: "bottom-right", Opacity: 0.6, Scale: 0.2}
	if m := validateImageParams(&valid); m != "" {
		t.Fatalf("valid PNG watermark rejected: %s", m)
	}
}

func TestValidateFileParams(t *testing.T) {
	for _, d := range preset.DefaultPresets() {
		if d.Kind != "file" {
			continue
		}
		var fp preset.FileParams
		_ = json.Unmarshal(d.Params, &fp)
		if m := validateFileParams(&fp); m != "" {
			t.Errorf("%s rejected: %s", d.Slug, m)
		}
		if fp.Text.Enabled {
			t.Errorf("%s default preset should have text.enabled=false", d.Slug)
		}
	}

	var fp preset.FileParams
	fp.Text = preset.FileText{Enabled: true}
	if m := validateFileParams(&fp); m != "" {
		t.Fatalf("default text.maxBytes rejected: %s", m)
	}
	if fp.Text.MaxBytes != 2<<20 {
		t.Fatalf("text.maxBytes = %d, want %d", fp.Text.MaxBytes, 2<<20)
	}
	for _, bad := range []int{512, 11 << 20} {
		fp.Text = preset.FileText{Enabled: true, MaxBytes: bad}
		if validateFileParams(&fp) == "" {
			t.Fatalf("accepted invalid text.maxBytes=%d", bad)
		}
	}
}

func TestPlaygroundHandler(t *testing.T) {
	s := &Server{cfg: Config{Playground: fakePlayground{fakeimage.New()}, TmpDir: t.TempDir()}, playSem: make(chan struct{}, 2)}
	var params preset.ImageParams
	for _, d := range preset.DefaultPresets() {
		if d.Slug == "photo-lite" {
			_ = json.Unmarshal(d.Params, &params)
		}
	}
	pj, _ := json.Marshal(params)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.jpg")
	fw.Write([]byte("\xff\xd8\xff\xe0 sample" + string(make([]byte, 500))))
	mw.WriteField("params", string(pj))
	mw.Close()
	req := httptest.NewRequest("POST", "/admin/api/playground", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handlePlayground(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Renditions []struct {
			SSIM float64
			Data string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Renditions) != 1 || out.Renditions[0].SSIM != 0.97 || out.Renditions[0].Data == "" {
		t.Fatalf("%v %s", err, rec.Body)
	}
	// Missing params → 422.
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("file", "a.jpg")
	fw.Write([]byte("x"))
	mw.Close()
	req = httptest.NewRequest("POST", "/admin/api/playground", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	s.handlePlayground(rec, req)
	if rec.Code != 422 {
		t.Fatalf("missing params: %d", rec.Code)
	}
}
