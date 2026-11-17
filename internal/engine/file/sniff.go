package file

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// ContentTypes of the supported formats.
var ContentTypes = map[string]string{
	"pdf":  "application/pdf",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"odt":  "application/vnd.oasis.opendocument.text",
	"ods":  "application/vnd.oasis.opendocument.spreadsheet",
	"odp":  "application/vnd.oasis.opendocument.presentation",
	"rtf":  "application/rtf",
	"txt":  "text/plain; charset=utf-8",
	"csv":  "text/csv; charset=utf-8",
}

// FormatForContentType maps a declared MIME type to a format ("" if unknown).
func FormatForContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	for f, c := range ContentTypes {
		if strings.SplitN(c, ";", 2)[0] == ct {
			return f
		}
	}
	return ""
}

const (
	maxZipEntries      = 20000
	maxZipUncompressed = 2 << 30 // 2 GiB
	maxZipRatio        = 1000
)

var errNotDocument = errors.New("not a supported document")

// sniff identifies the format from content. Zip-based formats are opened and
// inspected (bomb limits applied); macro-enabled Office files are refused.
func sniff(path, name string) (format string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.Contains(head[:min(len(head), 1024)], []byte("%PDF-")):
		return "pdf", nil
	case bytes.HasPrefix(head, []byte("{\\rtf")):
		return "rtf", nil
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return sniffZip(path)
	case bytes.HasPrefix(head, []byte{0xD0, 0xCF, 0x11, 0xE0}):
		return "", fmt.Errorf("%w: legacy binary Office formats (doc, xls, ppt) are not accepted; save as docx, xlsx or pptx", errNotDocument)
	}
	if looksLikeText(head) {
		// Markup (HTML, SVG, XML) is never a "text document": refuse it so it
		// cannot be smuggled in under a text name.
		if t := bytes.TrimLeft(bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF}), " \t\r\n"); len(t) > 0 && t[0] == '<' {
			return "", fmt.Errorf("%w: markup files (HTML, SVG, XML) are not accepted", errNotDocument)
		}
		if strings.HasSuffix(strings.ToLower(name), ".csv") {
			return "csv", nil
		}
		return "txt", nil
	}
	return "", errNotDocument
}

func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	// A multi-byte character may be cut at the end of the window.
	for i := 0; i < 4 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	if !utf8.Valid(b) {
		return false
	}
	ctrl := 0
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' {
			ctrl++
		}
	}
	return ctrl*100 <= len(b) // at most 1% control characters
}

func sniffZip(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNotDocument, err)
	}
	defer zr.Close()
	if len(zr.File) == 0 || len(zr.File) > maxZipEntries {
		return "", fmt.Errorf("%w: unexpected number of archive entries", errNotDocument)
	}
	var total uint64
	names := map[string]*zip.File{}
	for _, e := range zr.File {
		total += e.UncompressedSize64
		if e.CompressedSize64 > 0 && e.UncompressedSize64/e.CompressedSize64 > maxZipRatio && e.UncompressedSize64 > 1<<20 {
			return "", fmt.Errorf("%w: suspicious compression ratio", errNotDocument)
		}
		names[e.Name] = e
		if strings.HasSuffix(strings.ToLower(e.Name), "vbaproject.bin") {
			return "", fmt.Errorf("%w: macro-enabled documents are not accepted", errNotDocument)
		}
	}
	if total > maxZipUncompressed {
		return "", fmt.Errorf("%w: archive expands to more than %d bytes", errNotDocument, uint64(maxZipUncompressed))
	}
	if m := names["mimetype"]; m != nil { // OpenDocument: the first entry holds the media type
		rc, err := m.Open()
		if err != nil {
			return "", fmt.Errorf("%w: %v", errNotDocument, err)
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 128))
		rc.Close()
		switch strings.TrimSpace(string(b)) {
		case "application/vnd.oasis.opendocument.text":
			return "odt", nil
		case "application/vnd.oasis.opendocument.spreadsheet":
			return "ods", nil
		case "application/vnd.oasis.opendocument.presentation":
			return "odp", nil
		}
		return "", errNotDocument
	}
	if names["[Content_Types].xml"] != nil { // OOXML
		switch {
		case names["word/document.xml"] != nil:
			return "docx", nil
		case names["xl/workbook.xml"] != nil:
			return "xlsx", nil
		case names["ppt/presentation.xml"] != nil:
			return "pptx", nil
		}
	}
	return "", errNotDocument
}

// pdfMarkers scans the whole file for the markers that decide whether a PDF
// may be rewritten, and checks that it ends with %%EOF.
type pdfMarkers struct {
	Encrypted, Signed, PDFA, HasEOF bool
}

func scanPDF(path string) (pdfMarkers, error) {
	f, err := os.Open(path)
	if err != nil {
		return pdfMarkers{}, err
	}
	defer f.Close()
	var m pdfMarkers
	patterns := map[string]*bool{"/Encrypt": &m.Encrypted, "/ByteRange": &m.Signed, "pdfaid:part": &m.PDFA}
	const overlap = 16
	buf := make([]byte, 1<<20)
	var carry []byte
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			for p, flag := range patterns {
				if !*flag && bytes.Contains(chunk, []byte(p)) {
					*flag = true
				}
			}
			if len(chunk) > overlap {
				carry = append([]byte(nil), chunk[len(chunk)-overlap:]...)
			} else {
				carry = append([]byte(nil), chunk...)
			}
		}
		if err != nil {
			break
		}
	}
	st, err := f.Stat()
	if err != nil {
		return m, err
	}
	tail := make([]byte, min(st.Size(), 4096))
	if _, err := f.ReadAt(tail, st.Size()-int64(len(tail))); err != nil && !errors.Is(err, io.EOF) {
		return m, err
	}
	m.HasEOF = bytes.Contains(tail, []byte("%%EOF"))
	return m, nil
}
