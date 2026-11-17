package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3 implements Driver with aws-sdk-go-v2.
type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
}

var _ Driver = (*S3)(nil)

// NewS3 builds a driver for an S3-compatible endpoint.
func NewS3(cfg Config) (*S3, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("storage: endpoint is required")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("storage: invalid endpoint %q", cfg.Endpoint)
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("storage: credentials are required")
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	pathStyle := true
	if cfg.PathStyle != nil {
		pathStyle = *cfg.PathStyle
	}
	opts := s3.Options{
		Region:       region,
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
		BaseEndpoint: aws.String(strings.TrimRight(cfg.Endpoint, "/")),
		UsePathStyle: pathStyle,
		// R2 and several S3-compatible providers reject the newer default
		// CRC checksum headers; only send them when an operation needs them.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		RetryMaxAttempts:           3,
	}
	if cfg.HTTPClient != nil {
		opts.HTTPClient = cfg.HTTPClient
	}
	client := s3.New(opts)
	return &S3{client: client, presign: s3.NewPresignClient(client)}, nil
}

// Put uploads an object.
func (d *S3) Put(ctx context.Context, bucket, key string, body io.ReadSeeker, size int64, o PutOptions) error {
	in := &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: body, ContentLength: aws.Int64(size)}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.CacheControl != "" {
		in.CacheControl = aws.String(o.CacheControl)
	}
	if _, err := d.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("storage: put %s/%s: %w", bucket, key, mapErr(err))
	}
	return nil
}

// Head returns object metadata.
func (d *S3) Head(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	out, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("storage: head %s/%s: %w", bucket, key, mapErr(err))
	}
	return ObjectInfo{
		Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ETag: strings.Trim(aws.ToString(out.ETag), `"`), LastModified: aws.ToTime(out.LastModified),
	}, nil
}

// Get streams an object. The caller must close the body.
func (d *S3) Get(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error) {
	out, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("storage: get %s/%s: %w", bucket, key, mapErr(err))
	}
	return out.Body, ObjectInfo{
		Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ETag: strings.Trim(aws.ToString(out.ETag), `"`), LastModified: aws.ToTime(out.LastModified),
	}, nil
}

// Delete removes an object. Deleting a missing object is not an error (S3 semantics).
func (d *S3) Delete(ctx context.Context, bucket, key string) error {
	if _, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: &key}); err != nil {
		return fmt.Errorf("storage: delete %s/%s: %w", bucket, key, mapErr(err))
	}
	return nil
}

// Copy copies an object server-side (between buckets of the same account).
func (d *S3) Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) error {
	src := srcBucket + "/" + url.PathEscape(srcKey)
	if _, err := d.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &dstBucket, Key: &dstKey, CopySource: &src}); err != nil {
		return fmt.Errorf("storage: copy %s/%s -> %s/%s: %w", srcBucket, srcKey, dstBucket, dstKey, mapErr(err))
	}
	return nil
}

// PresignPut returns a presigned PUT URL with signed Content-Type and Content-Length.
func (d *S3) PresignPut(ctx context.Context, bucket, key string, o PresignPutOptions) (PresignedRequest, error) {
	ttl := o.TTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	in := &s3.PutObjectInput{Bucket: &bucket, Key: &key}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.ContentLength > 0 {
		in.ContentLength = aws.Int64(o.ContentLength)
	}
	req, err := d.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("storage: presign put %s/%s: %w", bucket, key, err)
	}
	return toPresigned(req.Method, req.URL, req.SignedHeader, ttl), nil
}

// PresignGet returns a presigned GET URL.
func (d *S3) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (PresignedRequest, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	req, err := d.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("storage: presign get %s/%s: %w", bucket, key, err)
	}
	return toPresigned(req.Method, req.URL, req.SignedHeader, ttl), nil
}

// CreateMultipart starts a multipart upload.
func (d *S3) CreateMultipart(ctx context.Context, bucket, key, contentType string) (MultipartUpload, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: &bucket, Key: &key}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	out, err := d.client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return MultipartUpload{}, fmt.Errorf("storage: create multipart %s/%s: %w", bucket, key, err)
	}
	return MultipartUpload{Bucket: bucket, Key: key, UploadID: aws.ToString(out.UploadId)}, nil
}

// PresignUploadPart returns a presigned PUT URL for one part.
func (d *S3) PresignUploadPart(ctx context.Context, u MultipartUpload, part int32, ttl time.Duration) (PresignedRequest, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	req, err := d.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: &u.Bucket, Key: &u.Key, UploadId: &u.UploadID, PartNumber: aws.Int32(part),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("storage: presign part %d of %s/%s: %w", part, u.Bucket, u.Key, err)
	}
	return toPresigned(req.Method, req.URL, req.SignedHeader, ttl), nil
}

// CompleteMultipart assembles the uploaded parts (sorted by number).
func (d *S3) CompleteMultipart(ctx context.Context, u MultipartUpload, parts []CompletedPart) error {
	sorted := append([]CompletedPart(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	cp := make([]types.CompletedPart, len(sorted))
	for i, p := range sorted {
		cp[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := d.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &u.Bucket, Key: &u.Key, UploadId: &u.UploadID, MultipartUpload: &types.CompletedMultipartUpload{Parts: cp},
	})
	if err != nil {
		return fmt.Errorf("storage: complete multipart %s/%s: %w", u.Bucket, u.Key, err)
	}
	return nil
}

// AbortMultipart discards the uploaded parts; aborting an unknown upload is not an error.
func (d *S3) AbortMultipart(ctx context.Context, u MultipartUpload) error {
	_, err := d.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &u.Bucket, Key: &u.Key, UploadId: &u.UploadID})
	var nsu *types.NoSuchUpload
	if err != nil && !errors.As(err, &nsu) {
		return fmt.Errorf("storage: abort multipart %s/%s: %w", u.Bucket, u.Key, err)
	}
	return nil
}

func toPresigned(method, u string, signed http.Header, ttl time.Duration) PresignedRequest {
	h := http.Header{}
	for k, v := range signed {
		// Host is set by the HTTP client from the URL.
		if strings.EqualFold(k, "Host") {
			continue
		}
		h[k] = append([]string(nil), v...)
	}
	return PresignedRequest{Method: method, URL: u, Headers: h, ExpiresAt: time.Now().Add(ttl)}
}

func mapErr(err error) error {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && (ae.ErrorCode() == "NotFound" || ae.ErrorCode() == "NoSuchKey") {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}
