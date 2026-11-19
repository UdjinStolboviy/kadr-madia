package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
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
	if o.ContentDisposition != "" {
		in.ContentDisposition = aws.String(o.ContentDisposition)
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
		ContentDisposition: aws.ToString(out.ContentDisposition),
	}, nil
}

// Get streams an object. The caller must close the body.
func (d *S3) Get(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error) {
	out, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("storage: get %s/%s: %w", bucket, key, mapErr(err))
	}
	return out.Body, objectInfo(key, out, aws.ToInt64(out.ContentLength)), nil
}

// GetRange streams bytes [offset, offset+length) of an object.
func (d *S3) GetRange(ctx context.Context, bucket, key string, offset, length int64) (io.ReadCloser, ObjectInfo, error) {
	if offset < 0 || length <= 0 {
		return nil, ObjectInfo{}, fmt.Errorf("storage: get range %s/%s: invalid range %d+%d", bucket, key, offset, length)
	}
	rng := fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	out, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key, Range: &rng})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("storage: get range %s/%s: %w", bucket, key, mapErr(err))
	}
	size := aws.ToInt64(out.ContentLength)
	if cr := aws.ToString(out.ContentRange); cr != "" { // bytes a-b/total
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				size = n
			}
		}
	}
	return out.Body, objectInfo(key, out, size), nil
}

func objectInfo(key string, out *s3.GetObjectOutput, size int64) ObjectInfo {
	return ObjectInfo{
		Key: key, Size: size, ContentType: aws.ToString(out.ContentType),
		ETag: strings.Trim(aws.ToString(out.ETag), `"`), LastModified: aws.ToTime(out.LastModified),
		ContentDisposition: aws.ToString(out.ContentDisposition),
	}
}

// Delete removes an object. Deleting a missing object is not an error (S3 semantics).
func (d *S3) Delete(ctx context.Context, bucket, key string) error {
	if _, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: &key}); err != nil {
		return fmt.Errorf("storage: delete %s/%s: %w", bucket, key, mapErr(err))
	}
	return nil
}

// List returns all objects under prefix, following pagination.
func (d *S3) List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	var token *string
	for {
		res, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &bucket, Prefix: &prefix, ContinuationToken: token})
		if err != nil {
			return nil, fmt.Errorf("storage: list %s/%s: %w", bucket, prefix, mapErr(err))
		}
		for _, o := range res.Contents {
			info := ObjectInfo{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), ETag: aws.ToString(o.ETag)}
			if o.LastModified != nil {
				info.LastModified = *o.LastModified
			}
			out = append(out, info)
		}
		if !aws.ToBool(res.IsTruncated) || res.NextContinuationToken == nil {
			return out, nil
		}
		token = res.NextContinuationToken
	}
}

// Copy copies an object server-side (between buckets of the same account).
func (d *S3) Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string, opts ...PutOptions) error {
	src := srcBucket + "/" + url.PathEscape(srcKey)
	in := &s3.CopyObjectInput{Bucket: &dstBucket, Key: &dstKey, CopySource: &src}
	if len(opts) > 0 {
		in.MetadataDirective = types.MetadataDirectiveReplace
		if opts[0].ContentType != "" {
			in.ContentType = aws.String(opts[0].ContentType)
		}
		if opts[0].CacheControl != "" {
			in.CacheControl = aws.String(opts[0].CacheControl)
		}
		if opts[0].ContentDisposition != "" {
			in.ContentDisposition = aws.String(opts[0].ContentDisposition)
		}
	}
	if _, err := d.client.CopyObject(ctx, in); err != nil {
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

// PresignDownload returns a presigned GET for direct reads by clients.
func (d *S3) PresignDownload(ctx context.Context, bucket, key string, o PresignGetOptions) (PresignedRequest, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	signAt, expires := now.UTC().Truncate(time.Second), ttl
	if o.StableWindow > 0 {
		// The same URL for the whole window: validity counts from the window
		// start and covers the window, so the remaining time is always >= TTL.
		signAt, expires = now.UTC().Truncate(o.StableWindow), ttl+o.StableWindow
	}
	if expires > MaxPresignTTL {
		expires = MaxPresignTTL
	}
	expires = expires.Truncate(time.Second)
	in := &s3.GetObjectInput{Bucket: &bucket, Key: &key}
	if o.ContentDisposition != "" {
		in.ResponseContentDisposition = aws.String(o.ContentDisposition)
	}
	pc := s3.NewPresignClient(d.client, func(po *s3.PresignOptions) {
		po.Expires = expires
		po.Presigner = fixedTimePresigner{inner: v4.NewSigner(func(so *v4.SignerOptions) { so.DisableURIPathEscaping = true }), at: signAt}
	})
	req, err := pc.PresignGetObject(ctx, in)
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("storage: presign download %s/%s: %w", bucket, key, err)
	}
	return PresignedRequest{Method: req.Method, URL: req.URL, Headers: req.SignedHeader, ExpiresAt: signAt.Add(expires)}, nil
}

// fixedTimePresigner signs with a given time instead of the current one.
type fixedTimePresigner struct {
	inner *v4.Signer
	at    time.Time
}

func (p fixedTimePresigner) PresignHTTP(ctx context.Context, c aws.Credentials, r *http.Request, payloadHash, service, region string,
	_ time.Time, optFns ...func(*v4.SignerOptions)) (string, http.Header, error) {
	return p.inner.PresignHTTP(ctx, c, r, payloadHash, service, region, p.at, optFns...)
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
