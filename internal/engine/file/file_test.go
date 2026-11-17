package file

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func writeZip(t *testing.T, dir, name string, files map[string]string, order ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(n, c string) {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(c))
	}
	for _, n := range order { // entries that must come first (OpenDocument mimetype)
		write(n, files[n])
		delete(files, n)
	}
	for n, c := range files {
		write(n, c)
	}
	zw.Close()
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// testPDF builds a valid PDF with one uncompressed RGB image (w×h) so that
// optimisation has something to chew on.
func testPDF(w, h int, pages int) []byte {
	rng := rand.New(rand.NewSource(1))
	img := make([]byte, w*h*3)
	for y := 0; y < h; y++ { // smooth gradient with mild noise: compresses, but not to nothing
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			img[i], img[i+1], img[i+2] = byte(x*255/w), byte(y*255/h), byte((x+y)*255/(w+h)+rng.Intn(12))
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
		content := "q 500 0 0 500 40 200 cm /Im0 Do Q"
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	offs = append(offs, buf.Len())
	fmt.Fprintf(&buf, "%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n",
		len(offs), w, h, len(img))
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

func limits() preset.FileLimits {
	return preset.FileLimits{MaxInputBytes: 50 << 20, Accept: preset.DefaultFileFormats}
}

func TestProbeIdentifiesByContent(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{Ghostscript: "no-such-gs", QPDF: "no-such-qpdf"})
	ctx := context.Background()
	cases := []struct {
		name string
		path string
		want string
	}{
		{"pdf", writeFile(t, dir, "a.bin", testPDF(8, 8, 1)), "pdf"},
		{"docx", writeZip(t, dir, "a.dat", map[string]string{"[Content_Types].xml": "<x/>", "word/document.xml": "<w/>"}), "docx"},
		{"xlsx", writeZip(t, dir, "b.dat", map[string]string{"[Content_Types].xml": "<x/>", "xl/workbook.xml": "<w/>"}), "xlsx"},
		{"pptx", writeZip(t, dir, "c.dat", map[string]string{"[Content_Types].xml": "<x/>", "ppt/presentation.xml": "<w/>"}), "pptx"},
		{"odt", writeZip(t, dir, "d.dat", map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": "<c/>"}, "mimetype"), "odt"},
		{"ods", writeZip(t, dir, "e.dat", map[string]string{"mimetype": "application/vnd.oasis.opendocument.spreadsheet"}, "mimetype"), "ods"},
		{"rtf", writeFile(t, dir, "f.dat", []byte(`{\rtf1\ansi hello}`)), "rtf"},
		{"txt", writeFile(t, dir, "g.dat", []byte("Привіт, світ!\nplain text\n")), "txt"},
	}
	for _, c := range cases {
		info, err := e.Probe(ctx, c.path, "upload", limits())
		if err != nil || info.Format != c.want || info.ContentType != ContentTypes[c.want] {
			t.Errorf("%s: %+v %v", c.name, info, err)
		}
	}
	// csv is a txt whose name says so.
	csv := writeFile(t, dir, "h.dat", []byte("a,b\n1,2\n"))
	if info, err := e.Probe(ctx, csv, "report.CSV", limits()); err != nil || info.Format != "csv" {
		t.Errorf("csv: %+v %v", info, err)
	}
}

func TestProbeRejects(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{Ghostscript: "no-such-gs"})
	ctx := context.Background()
	bomb := writeZip(t, dir, "bomb.docx", map[string]string{"[Content_Types].xml": "<x/>", "word/document.xml": string(bytes.Repeat([]byte("A"), 40<<20))})
	cases := map[string]struct {
		path string
		code string
	}{
		"html":       {writeFile(t, dir, "x.pdf", []byte("<html><script>alert(1)</script></html>")), CodeUnsupportedMedia},
		"svg":        {writeFile(t, dir, "y.pdf", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`)), CodeUnsupportedMedia},
		"exe":        {writeFile(t, dir, "z.pdf", append([]byte("MZ"), make([]byte, 400)...)), CodeUnsupportedMedia},
		"legacy doc": {writeFile(t, dir, "l.doc", append([]byte{0xD0, 0xCF, 0x11, 0xE0}, make([]byte, 600)...)), CodeUnsupportedMedia},
		"macro docm": {writeZip(t, dir, "m.docx", map[string]string{"[Content_Types].xml": "<x/>", "word/document.xml": "<w/>", "word/vbaProject.bin": "x"}), CodeUnsupportedMedia},
		"zip":        {writeZip(t, dir, "n.zip", map[string]string{"a.txt": "hello"}), CodeUnsupportedMedia},
		"bomb":       {bomb, CodeUnsupportedMedia},
		"truncated":  {writeFile(t, dir, "t.pdf", testPDF(8, 8, 1)[:200]), CodeCorrupt},
		"empty":      {writeFile(t, dir, "e.pdf", nil), CodeCorrupt},
		"binary":     {writeFile(t, dir, "b.txt", append([]byte("text"), 0, 1, 2, 3)), CodeUnsupportedMedia},
	}
	for name, c := range cases {
		_, err := e.Probe(ctx, c.path, name, limits())
		if ie, ok := AsError(err); !ok || ie.Code != c.code {
			t.Errorf("%s: %v (want %s)", name, err, c.code)
		}
	}
	// Limits.
	pdf := writeFile(t, dir, "big.pdf", testPDF(64, 64, 1))
	if _, err := e.Probe(ctx, pdf, "", preset.FileLimits{MaxInputBytes: 100, Accept: []string{"pdf"}}); err == nil {
		t.Error("size limit not enforced")
	}
	if _, err := e.Probe(ctx, pdf, "", preset.FileLimits{MaxInputBytes: 1 << 20, Accept: []string{"docx"}}); err == nil {
		t.Error("accept list not enforced")
	}
}

func TestPDFMarkers(t *testing.T) {
	dir := t.TempDir()
	base := testPDF(8, 8, 1)
	e := New(Config{Ghostscript: "no-such-gs"})
	mk := func(extra string) SourceInfo {
		data := append(append([]byte{}, base[:len(base)-6]...), []byte(extra+"\n%%EOF\n")...)
		info, err := e.Probe(context.Background(), writeFile(t, dir, "m.pdf", data), "", limits())
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if i := mk(""); i.Encrypted || i.Signed || i.PDFA {
		t.Fatalf("plain: %+v", i)
	}
	if !mk("/Encrypt 9 0 R").Encrypted || !mk("/ByteRange [0 1 2 3]").Signed || !mk("<pdfaid:part>1</pdfaid:part>").PDFA {
		t.Fatal("markers not detected")
	}
}

func TestProcessWithoutToolsStoresAsIs(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{Ghostscript: "no-such-gs", QPDF: "no-such-qpdf"})
	src := writeFile(t, dir, "a.pdf", testPDF(64, 64, 1))
	info, err := e.Probe(context.Background(), src, "", limits())
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []string{"downsample", "lossless"} {
		p := preset.FileParams{Kind: "file", Optimize: preset.FileOptimize{Strategy: strategy, ImageDPI: 100, JPEGQuality: 60}, Thumbnail: preset.FileThumb{Enabled: true, MaxSide: 400}}
		out, err := e.Process(context.Background(), info, p, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if out.Main.Optimized || out.Main.Path != src || out.Main.Skipped != "tool_missing" || out.ThumbPath != "" {
			t.Fatalf("%s: %+v", strategy, out)
		}
	}
	// Non-PDF documents are always stored as they are.
	docx := writeZip(t, dir, "a.docx", map[string]string{"[Content_Types].xml": "<x/>", "word/document.xml": "<w/>"})
	di, _ := e.Probe(context.Background(), docx, "", limits())
	out, err := e.Process(context.Background(), di, preset.FileParams{Optimize: preset.FileOptimize{Strategy: "lossless"}}, t.TempDir())
	if err != nil || out.Main.Path != docx || out.Main.Skipped != "not_pdf" {
		t.Fatalf("docx: %+v %v", out, err)
	}
}

func TestOptimizeWithRealTools(t *testing.T) {
	e := New(Config{})
	if !e.HasGhostscript() || !e.HasQPDF() {
		t.Skip("ghostscript and qpdf are required")
	}
	ctx := context.Background()
	dir := t.TempDir()
	src := writeFile(t, dir, "scan.pdf", testPDF(1200, 1200, 2))
	info, err := e.Probe(ctx, src, "", limits())
	if err != nil || info.Pages != 2 {
		t.Fatalf("probe: %+v %v", info, err)
	}
	sizes := map[string]int64{}
	for _, mode := range preset.Modes {
		var fp preset.FileParams
		for _, d := range preset.ModePresets() {
			if d.Slug == preset.ModeSlug("file", mode) {
				if err := jsonUnmarshal(d.Params, &fp); err != nil {
					t.Fatal(err)
				}
			}
		}
		work := t.TempDir()
		out, err := e.Process(ctx, info, fp, work)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if !out.Main.Optimized {
			t.Fatalf("%s: not optimised (%s)", mode, out.Main.Skipped)
		}
		if out.ThumbPath == "" {
			t.Fatalf("%s: no thumbnail", mode)
		}
		again, err := e.Probe(ctx, out.Main.Path, "", limits())
		if err != nil || again.Pages != 2 {
			t.Fatalf("%s: optimised file invalid: %+v %v", mode, again, err)
		}
		sizes[mode] = out.Main.Bytes
		t.Logf("%-8s %9d → %9d bytes (%.0f%%)", mode, info.Bytes, out.Main.Bytes, 100*float64(out.Main.Bytes)/float64(info.Bytes))
	}
	if !(sizes[preset.ModeCompact] < sizes[preset.ModeBalanced]) {
		t.Fatalf("compact must be smaller than balanced: %v", sizes)
	}
	// Signed PDFs are never rewritten.
	signed := append(append([]byte{}, testPDF(600, 600, 1)[:0]...), testPDF(600, 600, 1)...)
	signed = append(signed[:len(signed)-6], []byte("/ByteRange [0 1 2 3]\n%%EOF\n")...)
	sp := writeFile(t, dir, "signed.pdf", signed)
	si, err := e.Probe(ctx, sp, "", limits())
	if err != nil {
		t.Fatal(err)
	}
	so, err := e.Process(ctx, si, preset.FileParams{Optimize: preset.FileOptimize{Strategy: "downsample", ImageDPI: 72, JPEGQuality: 50}}, t.TempDir())
	if err != nil || so.Main.Optimized || so.Main.Skipped != "signed" {
		t.Fatalf("signed: %+v %v", so, err)
	}
}
