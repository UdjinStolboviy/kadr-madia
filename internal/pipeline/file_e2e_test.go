package pipeline_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/file"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

const (
	docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	pdfType  = "application/pdf"
)

func makeDocx(tag string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<w:document>" + tag + "</w:document>"} {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	return buf.Bytes()
}

func minimalPDF(tag string) []byte {
	return []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>\nendobj\n% " + tag + "\ntrailer\n<< /Root 1 0 R /Size 4 >>\n%%EOF\n")
}

func (e *env) uploadDoc(data []byte, ct, filename string, extra map[string]any) map[string]any {
	e.t.Helper()
	body := map[string]any{"namespace": "org_docs", "visibility": "private", "filename": filename, "content_type": ct, "size": len(data)}
	for k, v := range extra {
		body[k] = v
	}
	r := e.call("POST", "/v1/uploads", body, nil)
	expect(e.t, r, 201, "")
	tk := r.obj(e.t)
	e.putTicket(tk, data)
	c := e.call("POST", "/v1/uploads/"+tk["upload_id"].(string)+"/complete?wait=20s", nil, nil)
	expect(e.t, c, 200, "")
	return c.obj(e.t)
}

func TestDocumentUploads(t *testing.T) {
	e := newEnv(t)

	// DOCX with a mode: kind and preset come from the content type; stored as is.
	a := e.uploadDoc(makeDocx("one"), docxType, "Договір №1 \"final\".docx", map[string]any{"mode": "balanced"})
	if a["status"] != "ready" || a["kind"] != "file" || a["preset"] != "file-balanced" {
		t.Fatalf("docx: %v", a)
	}
	rends := a["renditions"].([]any)
	if len(rends) != 2 {
		t.Fatalf("docx renditions: %v", rends)
	}
	var main, text map[string]any
	for _, x := range rends {
		r := x.(map[string]any)
		switch r["name"] {
		case "main":
			main = r
		case "text":
			text = r
		}
	}
	if main == nil || main["format"] != "docx" || main["key"] == nil {
		t.Fatalf("main rendition: %v", rends)
	}
	if text == nil || text["format"] != "json" || text["key"] == nil {
		t.Fatalf("text rendition: %v", rends)
	}
	key := main["key"].(string)
	if !e.objectExists(privateBucket, key) {
		t.Fatalf("object %s missing", key)
	}
	opts, _ := e.driver.opts(privateBucket, key)
	if opts.ContentType != docxType || !strings.HasPrefix(opts.ContentDisposition, "attachment; filename=") ||
		strings.Count(opts.ContentDisposition, `"`) != 2 || strings.Contains(opts.ContentDisposition, `\`) {
		t.Fatalf("stored headers: %+v", opts)
	}
	if !strings.Contains(opts.ContentDisposition, "filename*=UTF-8''") {
		t.Fatalf("no UTF-8 filename: %q", opts.ContentDisposition)
	}

	// PDF via mode compact (file-compact); thumbnails appear only when Ghostscript is installed.
	p := e.uploadDoc(minimalPDF("p"), pdfType, "a.pdf", map[string]any{"mode": "compact"})
	if p["status"] != "ready" || p["preset"] != "file-compact" {
		t.Fatalf("pdf: %v", p)
	}

	// Default mode applies without preset/mode; the content type decides the kind.
	d := e.uploadDoc(minimalPDF("d"), pdfType, "d.pdf", nil)
	if d["preset"] != "file-balanced" {
		t.Fatalf("default mode → %v", d["preset"])
	}

	// A "PDF" that is really HTML fails by content, not by name.
	h := e.uploadDoc([]byte("<html><script>alert(1)</script></html>"), pdfType, "evil.pdf", map[string]any{"mode": "balanced"})
	if h["status"] != "failed" || h["error"].(map[string]any)["code"] != "unsupported_media" {
		t.Fatalf("html as pdf: %v", h)
	}

	// Dedup by content within the same preset.
	again := e.uploadDoc(makeDocx("one"), docxType, "copy.docx", map[string]any{"mode": "balanced"})
	if again["status"] != "ready" || again["id"] == a["id"] {
		t.Fatalf("dedup: %v", again)
	}

	// Images still work through the same mode and are not confused with documents.
	b := ticketBody(photo("x"), "org_docs", "private", false)
	delete(b, "preset")
	b["mode"] = "compact"
	r := e.call("POST", "/v1/uploads", b, nil)
	expect(t, r, 201, "")

	// A content type that is no document and no media is refused up front.
	bad := map[string]any{"namespace": "org_docs", "visibility": "private", "filename": "a.zip", "content_type": "application/zip", "size": 10, "mode": "balanced"}
	expect(t, e.call("POST", "/v1/uploads", bad, nil), 422, "validation_failed")

	// Asset listing can filter documents.
	l := e.call("GET", "/v1/assets?kind=file&namespace=org_docs", nil, nil)
	expect(t, l, 200, "")
	if n := len(l.obj(t)["items"].([]any)); n < 4 {
		t.Fatalf("kind=file listing returned %d items", n)
	}
}

// With the real engines (libvips + Ghostscript + qpdf) a scanned PDF is
// optimised and gets a compressed first-page thumbnail. Skipped without tools.
func TestDocumentPipelineWithRealEngines(t *testing.T) {
	fe := file.New(file.Config{})
	if !fe.HasGhostscript() || !fe.HasQPDF() {
		t.Skip("ghostscript and qpdf are required")
	}
	ie, err := image.NewVipsCLI(image.VipsConfig{Timeout: 60 * time.Second})
	if err != nil {
		t.Skip("libvips is required")
	}
	e := newEnv(t, func(c *pipeline.Config) { c.Engine = ie })
	data := scanPDF(1200, 1200, 2)
	a := e.uploadDoc(data, pdfType, "scan.pdf", map[string]any{"mode": "compact"})
	if a["status"] != "ready" {
		t.Fatalf("not ready: %v", a)
	}
	var mainBytes float64
	var thumbs int
	for _, x := range a["renditions"].([]any) {
		r := x.(map[string]any)
		switch r["name"] {
		case "main":
			mainBytes = r["bytes"].(float64)
		case "thumb":
			thumbs++
			if r["bytes"].(float64) > 12<<10 {
				t.Errorf("thumb %v is %v bytes, budget 12 KB", r["format"], r["bytes"])
			}
		}
	}
	if mainBytes == 0 || mainBytes > float64(len(data))/5 {
		t.Fatalf("main file %v bytes of %d: not optimised", mainBytes, len(data))
	}
	if thumbs != 1 { // default presets: AVIF-only thumbnail
		t.Fatalf("thumb renditions = %d", thumbs)
	}
	if a["placeholder"] == nil {
		t.Fatal("no placeholder for the thumbnail")
	}
	// Quality mode: lossless; the file stays a valid PDF.
	q := e.uploadDoc(data, pdfType, "scan.pdf", map[string]any{"mode": "quality"})
	if q["status"] != "ready" || q["preset"] != "file-quality" {
		t.Fatalf("quality: %v", q)
	}
}

func TestDocumentTextExtractionE2E(t *testing.T) {
	e := newEnv(t)

	// Create a preset with text extraction enabled.
	fp, _ := json.Marshal(preset.FileParams{
		Kind:      "file",
		Limits:    preset.FileLimits{MaxInputBytes: 50 << 20, Accept: preset.DefaultFileFormats},
		Optimize:  preset.FileOptimize{Strategy: preset.FileStrategyNone},
		Thumbnail: preset.FileThumb{Enabled: false},
		Text:      preset.FileText{Enabled: true, MaxBytes: 1 << 20},
	})
	must(t, e.store.CreatePreset(e.ctx, &db.Preset{
		ID: db.NewID(db.PrefixPreset), ProjectID: e.project.ID, Slug: "file-diff",
		Kind: "file", Description: "Text extraction", CurrentVersion: 1, Params: fp, CreatedAt: e.clock.Now(),
	}, "test"))

	docx := makeDocx(`<w:body><w:p><w:r><w:t>Contract clause one</w:t></w:r></w:p></w:body>`)
	a := e.uploadDoc(docx, docxType, "contract.docx", map[string]any{"preset": "file-diff"})
	if a["status"] != "ready" {
		t.Fatalf("docx not ready: %v", a)
	}
	var textKey string
	for _, x := range a["renditions"].([]any) {
		r := x.(map[string]any)
		if r["name"] == "text" && r["format"] == "json" {
			textKey = r["key"].(string)
		}
	}
	if textKey == "" {
		t.Fatalf("missing text.json rendition: %v", a["renditions"])
	}
	opts, _ := e.driver.opts(privateBucket, textKey)
	if opts.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("text.json content type: %q", opts.ContentType)
	}
	rc, _, err := e.driver.Get(e.ctx, privateBucket, textKey)
	if err != nil {
		t.Fatal(err)
	}
	rawJSON, _ := io.ReadAll(rc)
	rc.Close()
	var et file.ExtractedText
	if err := json.Unmarshal(rawJSON, &et); err != nil {
		t.Fatal(err)
	}
	if et.Format != "docx" || et.Text != "Contract clause one" {
		t.Fatalf("unexpected extracted text: %+v", et)
	}

	// Private text.json rendition can be signed via POST /v1/signed-urls.
	enc, err := e.kr.Encrypt([]byte("s3cret"), pipeline.SigningKeySecretAAD("doc-k1"))
	must(t, err)
	must(t, e.store.RotateSigningKey(e.ctx, &db.SigningKey{ID: "doc-k1", ProjectID: e.project.ID, SecretEnc: enc, CreatedAt: e.clock.Now()}))
	sres := e.call("POST", "/v1/signed-urls", map[string]any{
		"items": []map[string]any{{"asset_id": a["id"], "rendition": "text.json", "ttl": 300}},
	}, nil)
	expect(t, sres, 200, "")
	if len(sres.obj(t)["items"].([]any)) != 1 {
		t.Fatalf("signed-urls: %s", sres.body)
	}

	// Visibility change copies text.json to public bucket and preserves access.
	pres := e.call("PATCH", "/v1/assets/"+a["id"].(string), map[string]any{"visibility": "public"}, nil)
	expect(t, pres, 200, "")
	pubAsset := pres.obj(t)
	var pubTextURL string
	for _, x := range pubAsset["renditions"].([]any) {
		r := x.(map[string]any)
		if r["name"] == "text" && r["format"] == "json" {
			pubTextURL, _ = r["url"].(string)
		}
	}
	if pubTextURL == "" {
		t.Fatalf("missing public text.json URL after PATCH visibility: %v", pubAsset["renditions"])
	}
	pubKey := strings.TrimPrefix(pubTextURL, cdnBase+"/")
	if !e.objectExists(publicBucket, pubKey) {
		t.Fatalf("public text.json missing at %s", pubKey)
	}

	// Delete & GC cleans up text.json.
	expect(t, e.call("DELETE", "/v1/assets/"+a["id"].(string), nil, nil), 204, "")
	if _, err := e.svc.CollectGarbage(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.objectExists(publicBucket, pubKey) {
		t.Fatalf("public text.json still exists after GC")
	}

	// Verify PDF extraction if pdftotext is installed on host.
	if file.New(file.Config{}).HasPDFToText() {
		pdfDoc := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
			"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n" +
			"4 0 obj\n<< /Length 41 >>\nstream\nBT /F1 12 Tf 20 200 Td (PDF Clause 1) Tj ET\nendstream\nendobj\n" +
			"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n" +
			"trailer\n<< /Size 6 /Root 1 0 R >>\n%%EOF\n")
		pAsset := e.uploadDoc(pdfDoc, pdfType, "clause.pdf", map[string]any{"preset": "file-diff"})
		if pAsset["status"] != "ready" {
			t.Fatalf("pdf status: %v", pAsset)
		}
		var pdfTextKey string
		for _, x := range pAsset["renditions"].([]any) {
			r := x.(map[string]any)
			if r["name"] == "text" && r["format"] == "json" {
				pdfTextKey = r["key"].(string)
			}
		}
		if pdfTextKey == "" {
			t.Fatalf("expected text.json for PDF: %v", pAsset["renditions"])
		}
		prc, _, err := e.driver.Get(e.ctx, privateBucket, pdfTextKey)
		if err != nil {
			t.Fatal(err)
		}
		pb, _ := io.ReadAll(prc)
		prc.Close()
		var pet file.ExtractedText
		if err := json.Unmarshal(pb, &pet); err != nil {
			t.Fatal(err)
		}
		if pet.Format != "pdf" || !strings.Contains(pet.Text, "PDF Clause 1") {
			t.Fatalf("unexpected PDF extracted text: %+v", pet)
		}
	}
	_ = http.StatusOK
}

// scanPDF builds a PDF with one large uncompressed RGB image per page.
func scanPDF(w, h, pages int) []byte {
	img := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			img[i], img[i+1], img[i+2] = byte(x*255/w), byte(y*255/h), byte((x+y)*255/(w+h))
		}
	}
	var buf bytes.Buffer
	var offs []int
	obj := func(body string) {
		offs = append(offs, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	buf.WriteString("%PDF-1.4\n")
	kids := ""
	for i := 0; i < pages; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+i*2)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pages))
	for i := 0; i < pages; i++ {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents %d 0 R /Resources << /XObject << /Im0 %d 0 R >> >> >>", 4+i*2, 3+pages*2))
		c := "q 500 0 0 500 40 200 cm /Im0 Do Q"
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c))
	}
	offs = append(offs, buf.Len())
	fmt.Fprintf(&buf, "%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n", len(offs), w, h, len(img))
	buf.Write(img)
	buf.WriteString("\nendstream\nendobj\n")
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return buf.Bytes()
}
