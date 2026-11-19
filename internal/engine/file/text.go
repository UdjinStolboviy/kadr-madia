package file

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxTextBytes = 2 << 20
	maxXMLTextReadBytes = 32 << 20
)

func (t *Tools) extractTextFile(ctx context.Context, cpu *int64, workDir string, src SourceInfo, maxBytes int) (string, error) {
	et, err := t.extractText(ctx, cpu, workDir, src, maxBytes)
	if err != nil || et == nil {
		return "", err
	}
	b, err := json.Marshal(et)
	if err != nil {
		return "", err
	}
	outPath := filepath.Join(workDir, "text.json")
	if err := os.WriteFile(outPath, b, 0o600); err != nil {
		return "", err
	}
	return outPath, nil
}

func (t *Tools) extractText(ctx context.Context, cpu *int64, dir string, src SourceInfo, maxBytes int) (*ExtractedText, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxTextBytes
	}
	var pages []TextPage
	var preTruncated bool
	var err error

	switch src.Format {
	case "pdf":
		if src.Encrypted || t.pdftotext == "" {
			return nil, nil
		}
		pages, err = t.extractPDFText(ctx, cpu, dir, src.Path)
	case "docx":
		pages, err = extractDOCXText(src.Path)
	case "odt":
		pages, err = extractODTText(src.Path)
	case "txt", "csv":
		pages, preTruncated, err = extractPlainText(src.Path, maxBytes)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return boundExtractedText(src.Format, pages, maxBytes, preTruncated), nil
}

func (t *Tools) extractPDFText(ctx context.Context, cpu *int64, dir, path string) ([]TextPage, error) {
	raw, err := t.run(ctx, cpu, dir, t.cfg.TextTimeout, t.pdftotext, []string{"-enc", "UTF-8", "-eol", "unix", path, "-"})
	if err != nil {
		return nil, err
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.Split(raw, "\f")
	// pdftotext terminates the final page with a form feed.
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	pages := make([]TextPage, len(parts))
	for i, p := range parts {
		pages[i] = TextPage{Number: i + 1, Text: strings.TrimRight(p, "\r\n")}
	}
	return pages, nil
}

func extractPlainText(path string, maxBytes int) ([]TextPage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+4))
	if err != nil {
		return nil, false, err
	}
	buf = bytes.TrimPrefix(buf, []byte{0xEF, 0xBB, 0xBF})
	truncated := len(buf) > maxBytes
	s := strings.ReplaceAll(string(buf), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if truncated {
		s, _ = truncateUTF8(s, maxBytes)
	}
	return []TextPage{{Number: 1, Text: strings.TrimRight(s, "\n")}}, truncated, nil
}

func extractDOCXText(path string) ([]TextPage, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	rc, err := openZipEntry(zr, "word/document.xml")
	if err != nil || rc == nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(io.LimitReader(rc, maxXMLTextReadBytes))
	var sb strings.Builder
	var inText, inRow, inCell bool
	var cellIdx, paraInCell int

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "t":
				inText = true
			case "tab":
				sb.WriteByte('\t')
			case "br", "cr":
				sb.WriteByte('\n')
			case "tr":
				inRow = true
				cellIdx = 0
			case "tc":
				if inRow && cellIdx > 0 {
					sb.WriteByte('\t')
				}
				inCell = true
				paraInCell = 0
				cellIdx++
			case "p":
				if inCell && paraInCell > 0 {
					sb.WriteByte(' ')
				}
				if inCell {
					paraInCell++
				}
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "t":
				inText = false
			case "p":
				if !inCell {
					sb.WriteByte('\n')
				}
			case "tc":
				inCell = false
			case "tr":
				inRow = false
				sb.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				sb.Write(el)
			}
		}
	}
	text := strings.TrimRight(sb.String(), "\r\n")
	return []TextPage{{Number: 1, Text: text}}, nil
}

func extractODTText(path string) ([]TextPage, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	rc, err := openZipEntry(zr, "content.xml")
	if err != nil || rc == nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(io.LimitReader(rc, maxXMLTextReadBytes))
	var sb strings.Builder
	paraDepth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "p", "h":
				paraDepth++
			case "tab":
				if paraDepth > 0 {
					sb.WriteByte('\t')
				}
			case "line-break":
				if paraDepth > 0 {
					sb.WriteByte('\n')
				}
			case "s":
				if paraDepth > 0 {
					n := 1
					for _, a := range el.Attr {
						if a.Name.Local == "c" {
							if v, err := strconv.Atoi(a.Value); err == nil && v > 0 {
								n = min(v, 64)
							}
						}
					}
					sb.WriteString(strings.Repeat(" ", n))
				}
			}
		case xml.EndElement:
			if el.Name.Local == "p" || el.Name.Local == "h" {
				if paraDepth > 0 {
					paraDepth--
				}
				if paraDepth == 0 {
					sb.WriteByte('\n')
				}
			}
		case xml.CharData:
			if paraDepth > 0 {
				sb.Write(el)
			}
		}
	}
	text := strings.TrimRight(sb.String(), "\r\n")
	return []TextPage{{Number: 1, Text: text}}, nil
}

func openZipEntry(zr *zip.ReadCloser, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, nil
}

func boundExtractedText(format string, pages []TextPage, maxBytes int, preTruncated bool) *ExtractedText {
	if len(pages) == 0 {
		pages = []TextPage{{Number: 1, Text: ""}}
	}
	truncated := preTruncated
	var kept []TextPage
	used := 0
	for _, p := range pages {
		if used+len(p.Text) <= maxBytes {
			kept = append(kept, p)
			used += len(p.Text)
			continue
		}
		rem := max(0, maxBytes-used)
		part, cut := truncateUTF8(p.Text, rem)
		if cut || len(p.Text) > rem {
			truncated = true
		}
		if part != "" || len(kept) == 0 {
			kept = append(kept, TextPage{Number: p.Number, Text: part})
		}
		break
	}
	var parts []string
	for _, p := range kept {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	full := strings.Join(parts, "\n\n")
	if len(full) > maxBytes {
		full, _ = truncateUTF8(full, maxBytes)
		truncated = true
	}
	return &ExtractedText{
		Format:    format,
		Pages:     kept,
		Text:      full,
		Truncated: truncated,
	}
}

func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	if maxBytes <= 0 {
		return "", len(s) > 0
	}
	n := maxBytes
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n], true
}
