// Package file validates and optimises documents (PDF, Office, OpenDocument,
// RTF, text). Files are identified by their content, never by their name or
// declared type; PDFs can be optimised with Ghostscript/qpdf and get a
// first-page thumbnail. Everything optional degrades gracefully: without the
// tools a document is simply stored as uploaded.
package file

import (
	"context"
	"errors"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Engine processes documents.
type Engine interface {
	// Probe identifies the file at path by content and enforces limits. name
	// (the original file name, may be empty) only disambiguates csv from txt.
	Probe(ctx context.Context, path, name string, limits preset.FileLimits) (SourceInfo, error)
	// Process optimises the document per params. The main file is either the
	// source itself (Output.Main.Path == src.Path, never modified) or a new
	// file in workDir; ThumbPath (PNG) is set for PDFs when enabled.
	Process(ctx context.Context, src SourceInfo, params preset.FileParams, workDir string) (*Output, error)
}

// SourceInfo describes a validated document.
type SourceInfo struct {
	Path        string
	Format      string // pdf docx xlsx pptx odt ods odp rtf txt csv
	ContentType string
	Bytes       int64
	Pages       int  // PDF page count (0 when unknown)
	Encrypted   bool // PDF with /Encrypt
	Signed      bool // PDF with a digital signature (/ByteRange)
	PDFA        bool // PDF/A claim (pdfaid in XMP)
}

// Output is the result of Process.
type Output struct {
	Main      MainFile
	ThumbPath string // PNG of the first page, "" when none
	TextPath  string // UTF-8 extracted text JSON (text.json), "" when disabled or unavailable
	CPUMillis int64
}

// ExtractedText is the schema written to text.json when text extraction runs.
type ExtractedText struct {
	Format    string     `json:"format"`
	Pages     []TextPage `json:"pages"`
	Text      string     `json:"text"`
	Truncated bool       `json:"truncated"`
}

// TextPage is one extracted page (1-based). Formats without page breaks
// (DOCX, ODT, TXT, CSV) produce a single page.
type TextPage struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// MainFile is the stored document.
type MainFile struct {
	Path          string
	Format        string
	ContentType   string
	Bytes         int64
	OriginalBytes int64
	Optimized     bool   // true when a rewritten file replaced the original
	Skipped       string // why optimisation did not apply: signed, encrypted, pdfa, no_savings, tool_missing, failed, not_pdf, disabled
}

// Error codes surfaced as asset.failed error.code.
const (
	CodeUnsupportedMedia = "unsupported_media"
	CodeTooLarge         = "too_large"
	CodeCorrupt          = "corrupt"
	CodeTimeout          = "timeout"
	CodeEngineFailure    = "engine_failure"
)

// Error is a classified engine error.
type Error struct {
	Code      string
	Detail    string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Detail + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Detail
}

func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a *Error from err.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
