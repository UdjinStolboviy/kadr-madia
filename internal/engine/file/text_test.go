package file

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func buildTestZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "doc.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readExtractedJSON(t *testing.T, path string) ExtractedText {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var et ExtractedText
	if err := json.Unmarshal(b, &et); err != nil {
		t.Fatal(err)
	}
	return et
}

func TestExtractDOCXText(t *testing.T) {
	docx := buildTestZip(t, map[string]string{
		"[Content_Types].xml": "<Types/>",
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Договір №1</w:t></w:r></w:p>
    <w:p><w:r><w:t>Перший</w:t><w:br/><w:t>Другий</w:t></w:r></w:p>
    <w:tbl>
      <w:tr><w:tc><w:p><w:r><w:t>А1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Б1</w:t></w:r></w:p></w:tc></w:tr>
    </w:tbl>
  </w:body>
</w:document>`,
	})
	eng := New(Config{Ghostscript: "no-gs", QPDF: "no-qpdf", PDFToText: "no-pdftotext"})
	info, err := eng.Probe(context.Background(), docx, "contract.docx", preset.FileLimits{MaxInputBytes: 10 << 20, Accept: preset.DefaultFileFormats})
	if err != nil || info.Format != "docx" {
		t.Fatalf("probe: %+v %v", info, err)
	}
	outDir := t.TempDir()
	out, err := eng.Process(context.Background(), info, preset.FileParams{
		Kind: "file",
		Text: preset.FileText{Enabled: true, MaxBytes: 1 << 20},
	}, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if out.TextPath == "" {
		t.Fatal("expected TextPath for DOCX")
	}
	et := readExtractedJSON(t, out.TextPath)
	want := "Договір №1\nПерший\nДругий\nА1\tБ1"
	if et.Format != "docx" || et.Truncated || len(et.Pages) != 1 || et.Text != want {
		t.Fatalf("got %+v, want text %q", et, want)
	}
}

func TestExtractODTText(t *testing.T) {
	odt := buildTestZip(t, map[string]string{
		"mimetype": "application/vnd.oasis.opendocument.text",
		"content.xml": `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
  <office:body><office:text>
    <text:h>Заголовок</text:h>
    <text:p>Привіт<text:s text:c="2"/>світ<text:tab/>ок<text:line-break/>рядок</text:p>
  </office:text></office:body>
</office:document-content>`,
	})
	eng := New(Config{})
	info, err := eng.Probe(context.Background(), odt, "doc.odt", preset.FileLimits{MaxInputBytes: 10 << 20, Accept: preset.DefaultFileFormats})
	if err != nil || info.Format != "odt" {
		t.Fatalf("probe: %+v %v", info, err)
	}
	out, err := eng.Process(context.Background(), info, preset.FileParams{
		Kind: "file",
		Text: preset.FileText{Enabled: true},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	et := readExtractedJSON(t, out.TextPath)
	want := "Заголовок\nПривіт  світ\tок\nрядок"
	if et.Format != "odt" || et.Text != want {
		t.Fatalf("got %+v, want %q", et, want)
	}
}

func TestExtractPlainTextUTF8Truncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	// 2-byte Cyrillic chars so byte boundary cuts mid-rune unless handled safely.
	content := strings.Repeat("Ї", 60)
	if err := os.WriteFile(p, []byte("\xEF\xBB\xBF"+content), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := New(Config{})
	info, err := eng.Probe(context.Background(), p, "notes.txt", preset.FileLimits{MaxInputBytes: 10 << 20, Accept: preset.DefaultFileFormats})
	if err != nil {
		t.Fatal(err)
	}
	out, err := eng.Process(context.Background(), info, preset.FileParams{
		Kind: "file",
		Text: preset.FileText{Enabled: true, MaxBytes: 15},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	et := readExtractedJSON(t, out.TextPath)
	if !et.Truncated || len(et.Text) > 15 || et.Text != strings.Repeat("Ї", 7) {
		t.Fatalf("unexpected truncation: %+v (len=%d)", et, len(et.Text))
	}
}

func TestExtractPDFText(t *testing.T) {
	eng := New(Config{})
	if !eng.HasPDFToText() {
		t.Skip("pdftotext not installed")
	}
	pdfPath := filepath.Join(t.TempDir(), "hello.pdf")
	if err := os.WriteFile(pdfPath, textPDF("Hello Page 1", "Hello Page 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := eng.Probe(context.Background(), pdfPath, "hello.pdf", preset.FileLimits{MaxInputBytes: 10 << 20, Accept: preset.DefaultFileFormats})
	if err != nil {
		t.Fatal(err)
	}
	out, err := eng.Process(context.Background(), info, preset.FileParams{
		Kind: "file",
		Text: preset.FileText{Enabled: true},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out.TextPath == "" {
		t.Fatal("expected TextPath for PDF")
	}
	et := readExtractedJSON(t, out.TextPath)
	if et.Format != "pdf" || len(et.Pages) != 2 || et.Pages[0].Text != "Hello Page 1" || et.Pages[1].Text != "Hello Page 2" {
		t.Fatalf("unexpected PDF text: %+v", et)
	}

	// Missing pdftotext degrades gracefully.
	noTool := New(Config{Ghostscript: "no-gs", QPDF: "no-qpdf", PDFToText: "no-pdftotext"})
	out2, err := noTool.Process(context.Background(), info, preset.FileParams{
		Kind: "file",
		Text: preset.FileText{Enabled: true},
	}, t.TempDir())
	if err != nil || out2.TextPath != "" {
		t.Fatalf("missing pdftotext: out=%+v err=%v", out2, err)
	}

	// Encrypted PDF skips text extraction gracefully.
	encInfo := info
	encInfo.Encrypted = true
	out3, err := eng.Process(context.Background(), encInfo, preset.FileParams{
		Kind: "file",
		Text: preset.FileText{Enabled: true},
	}, t.TempDir())
	if err != nil || out3.TextPath != "" {
		t.Fatalf("encrypted PDF: out=%+v err=%v", out3, err)
	}
}

func textPDF(p1, p2 string) []byte {
	var buf bytes.Buffer
	var offs []int
	obj := func(body string) {
		offs = append(offs, buf.Len())
		buf.WriteString(string(rune('0'+len(offs))) + " 0 obj\n" + body + "\nendobj\n")
	}
	buf.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Resources << /Font << /F1 7 0 R >> >> /Contents 4 0 R >>")
	s1 := "BT /F1 12 Tf 20 200 Td (" + p1 + ") Tj ET"
	obj("<< /Length " + itoa(len(s1)) + " >>\nstream\n" + s1 + "\nendstream")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Resources << /Font << /F1 7 0 R >> >> /Contents 6 0 R >>")
	s2 := "BT /F1 12 Tf 20 200 Td (" + p2 + ") Tj ET"
	obj("<< /Length " + itoa(len(s2)) + " >>\nstream\n" + s2 + "\nendstream")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xref := buf.Len()
	buf.WriteString("xref\n0 8\n0000000000 65535 f \n")
	for _, o := range offs {
		// format 10-digit offset cleanly via Sprintf-free helper? Actually just import fmt.
		s := "0000000000" + itoa(o)
		buf.WriteString(s[len(s)-10:] + " 00000 n \n")
	}
	buf.WriteString("trailer\n<< /Size 8 /Root 1 0 R >>\nstartxref\n" + itoa(xref) + "\n%%EOF\n")
	return buf.Bytes()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
