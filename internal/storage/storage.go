// Package storage abstracts S3-compatible object storage (Cloudflare R2,
// AWS S3, Backblaze B2, Hetzner, Garage). Drivers are created per storage
// profile; buckets are passed explicitly so one driver serves both the public
// and the private bucket of a profile.
package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// ErrNotImplemented is returned by operations planned for later milestones.
var ErrNotImplemented = errors.New("storage: not implemented")

// Config describes how to reach a storage account.
type Config struct {
	Provider        string // r2 | s3 | b2 | hetzner | garage
	Endpoint        string // https://<account>.r2.cloudflarestorage.com
	Region          string // "auto" for R2
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle forces path-style addressing (bucket in the path). R2, Garage
	// and most self-hosted providers want it; defaults to true.
	PathStyle *bool
	// HTTPClient overrides the transport (tests).
	HTTPClient *http.Client
}

// ObjectInfo is the metadata of a stored object.
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// PutOptions are optional object attributes for Put.
type PutOptions struct {
	ContentType  string
	CacheControl string
}

// PresignPutOptions constrain a presigned upload. ContentLength and
// ContentType are signed, so the client cannot change them.
type PresignPutOptions struct {
	ContentType   string
	ContentLength int64
	TTL           time.Duration
}

// PresignedRequest is a URL plus the headers the client must send.
type PresignedRequest struct {
	Method    string
	URL       string
	Headers   http.Header
	ExpiresAt time.Time
}

// MultipartUpload identifies an in-progress multipart upload.
type MultipartUpload struct {
	Bucket   string
	Key      string
	UploadID string
}

// CompletedPart is one uploaded part of a multipart upload.
type CompletedPart struct {
	Number int32
	ETag   string
}

// Driver is the storage abstraction used by the pipeline and the admin API.
type Driver interface {
	Put(ctx context.Context, bucket, key string, body io.ReadSeeker, size int64, opts PutOptions) error
	Head(ctx context.Context, bucket, key string) (ObjectInfo, error)
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error)
	Delete(ctx context.Context, bucket, key string) error
	// List returns every object whose key starts with prefix (all pages).
	List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)
	// Copy copies server-side. With opts the destination gets that Content-Type
	// and Cache-Control instead of the source metadata.
	Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string, opts ...PutOptions) error
	PresignPut(ctx context.Context, bucket, key string, opts PresignPutOptions) (PresignedRequest, error)
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (PresignedRequest, error)

	// Multipart (implemented in M2).
	CreateMultipart(ctx context.Context, bucket, key, contentType string) (MultipartUpload, error)
	PresignUploadPart(ctx context.Context, u MultipartUpload, part int32, ttl time.Duration) (PresignedRequest, error)
	CompleteMultipart(ctx context.Context, u MultipartUpload, parts []CompletedPart) error
	AbortMultipart(ctx context.Context, u MultipartUpload) error
}
