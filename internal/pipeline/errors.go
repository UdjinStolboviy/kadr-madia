package pipeline

import (
	"fmt"
	"net/http"
)

// Error is an API-facing error: the HTTP layer renders it as problem+json.
type Error struct {
	Status int
	Code   string
	Detail string
	Fields map[string]string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

func apiErr(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Detail: fmt.Sprintf(format, args...)}
}

func validationErr(fields map[string]string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Detail: "request validation failed", Fields: fields}
}

// Error codes returned by the /v1 API (docs/API.md).
const (
	CodeQuotaExceeded    = "quota_exceeded"
	CodeUnsupportedMedia = "unsupported_media"
	CodeTooLarge         = "too_large"
	CodePresetNotFound   = "preset_not_found"
	CodeNotFound         = "not_found"
	CodeUploadIncomplete = "upload_incomplete"
	CodeUploadExpired    = "upload_expired"
	CodeUploadAborted    = "upload_aborted"
	CodeUploadNotPending = "upload_not_pending"
	CodeSizeMismatch     = "size_mismatch"
	CodeAssetProcessing  = "asset_processing"
	CodeNotImplemented   = "not_implemented"
	CodeStorageMissing   = "storage_not_configured"
	CodeStorageError     = "storage_error"
)

// Processing failure codes (asset.failed error.code) in addition to the
// engine codes of internal/engine/image.
const (
	FailSourceMissing    = "source_missing"
	FailChecksumMismatch = "checksum_mismatch"
	FailInternal         = "internal"
)

// jobError is a classified processing error. It carries the asset.failed
// error code and implements queue.CodedError for metrics.
type jobError struct {
	code   string
	detail string
	err    error
}

func (e *jobError) Error() string {
	if e.err != nil {
		return e.code + ": " + e.detail + ": " + e.err.Error()
	}
	return e.code + ": " + e.detail
}

func (e *jobError) Unwrap() error     { return e.err }
func (e *jobError) ErrorCode() string { return e.code }
