package admin

import (
	"bytes"
	"context"
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
