// Package pipeline implements the media lifecycle upload → asset →
// blob/renditions (docs/ARCHITECTURE.md §5.1) on top of the queue, the image
// engine and the storage drivers.
//
// Upload state machine (uploads.status):
//
//	pending ──complete/sweeper (object present)──► uploaded ──job claimed──► processing ──► done
//	   │                                              │                          └──────► failed
//	   ├──DELETE /v1/uploads/{id}──► aborted          └──(server-side uploads start here)
//	   └──sweeper (no object)──────► expired
//
// Asset state machine (assets.status): processing → ready | failed → deleted.
// Blob state machine (blobs.status): ready → gc_pending (refcount 0) → deleted.
//
// Quota: a ticket reserves declared bytes on the project and the namespace
// (reserved_bytes). Leaving an active upload state releases the reservation;
// a ready asset adds the bytes of its renditions to used_bytes. Every state
// change and its webhook event are committed in one transaction (outbox).
package pipeline

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/file"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/video"
	"github.com/UdjinStolboviy/kadr-madia/internal/importer"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

// Job kinds owned by the pipeline.
const (
	JobProcessImage = "process_image"
	JobProcessVideo = "process_video"
	JobProcessFile  = "process_file"
	JobSweepUploads = "sweep_uploads"
	JobGCBlobs      = "gc_blobs"
	JobGCBlob       = "gc_blob"
	JobRetention    = "retention"
)

// Limits of the M1 API.
const (
	MaxPutBytes               = 64 << 20 // image PUT uploads
	MaxVideoPutBytes          = 5 << 30
	MaxFilePutBytes           = 5 << 30 // single presigned PUT limit of S3/R2; video goes straight to R2
	MaxServerUploadBytes      = 25 << 20 // POST /v1/assets
	MaxMetadataBytes          = 16 << 10
	MinMultipartPartBytes     = 5 << 20  // S3/R2 minimum multipart part size
	DefaultMultipartThreshold = 16 << 20 // non-image uploads larger than this use S3 multipart
	DefaultPartSize           = 8 << 20
	MaxWait                   = 30 * time.Second
	DefaultUploadTTL          = 15 * time.Minute
	DefaultMultipartPartTTL   = time.Hour
	DefaultMultipartSessionTTL = 24 * time.Hour
	// DefaultSweepGrace delays the sweeper after ticket expiry so a PUT
	// started just before expiry can finish.
	DefaultSweepGrace = 5 * time.Minute
)

// Cache-Control of stored renditions (immutable, versioned URLs).
const (
	CacheControlPublic  = "public, max-age=31536000, immutable"
	CacheControlPrivate = "private, max-age=31536000, immutable"
)

// Notifier is told when new webhook deliveries were committed.
type Notifier interface{ Notify() }

// Config holds the dependencies of the pipeline.
type Config struct {
	Store  *db.Store
	Queue  *queue.Queue
	Engine image.Engine
	// Video is the video engine (nil disables video presets).
	Video video.Engine
	// File is the document engine (nil → document uploads fail with a retryable engine error).
	File    file.Engine
	Keyring *crypto.Keyring
	Logger  *slog.Logger
	Metrics *metrics.Set
	// TmpDir holds per-job work directories and staged server-side uploads.
	TmpDir string
	// Webhooks is woken after events are committed (optional).
	Webhooks Notifier
	// NewDriver builds a storage driver (default storage.NewS3).
	NewDriver func(storage.Config) (storage.Driver, error)
	// UploadTTL is the lifetime of an upload ticket (default 15 min).
	UploadTTL  time.Duration
	SweepGrace time.Duration
	Now        func() time.Time
	// MultipartThreshold: non-image uploads larger than this use S3 multipart
	// (default 16 MB). PartSize is the minimum part size (default 8 MB, min 5 MB).
	MultipartThreshold  int64
	MultipartPartTTL    time.Duration
	MultipartSessionTTL time.Duration
	// EdgeBaseURL is the private delivery host used by SignURLs.
	EdgeBaseURL string
	// Fetcher downloads URL imports; Rclone cloud imports (nil → defaults).
	Fetcher  *importer.Fetcher
	Rclone   *importer.Rclone
	PartSize int64
}

// Service is the pipeline.
type Service struct {
	store                   *db.Store
	queue                   *queue.Queue
	engine                  image.Engine
	video                   video.Engine
	file                    file.Engine
	keyring                 *crypto.Keyring
	log                     *slog.Logger
	metrics                 *metrics.Set
	tmpDir                  string
	webhooks                Notifier
	newDriver               func(storage.Config) (storage.Driver, error)
	uploadTTL               time.Duration
	mpPartTTL, mpSessionTTL time.Duration
	grace                   time.Duration
	mpThreshold, partSize   int64
	edgeBaseURL             string
	fetcher                 *importer.Fetcher
	rclone                  *importer.Rclone
	now                     func() time.Time

	drivers  drivers
	notifier *notifier

	bytesIn, bytesStored, dedupHits *metrics.Counter
}

// New builds the pipeline service.
func New(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = metrics.NewSet()
	}
	if cfg.NewDriver == nil {
		cfg.NewDriver = func(c storage.Config) (storage.Driver, error) { return storage.NewS3(c) }
	}
	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = DefaultUploadTTL
	}
	if cfg.SweepGrace <= 0 {
		cfg.SweepGrace = DefaultSweepGrace
	}
	if cfg.MultipartThreshold <= 0 {
		cfg.MultipartThreshold = DefaultMultipartThreshold
	}
	if cfg.PartSize <= 0 {
		cfg.PartSize = DefaultPartSize
	}
	if cfg.PartSize < MinMultipartPartBytes {
		cfg.PartSize = MinMultipartPartBytes
	}
	if cfg.MultipartPartTTL <= 0 {
		cfg.MultipartPartTTL = DefaultMultipartPartTTL
	}
	if cfg.MultipartSessionTTL <= 0 {
		cfg.MultipartSessionTTL = DefaultMultipartSessionTTL
	}
	if cfg.Fetcher == nil {
		cfg.Fetcher = &importer.Fetcher{}
	}
	if cfg.Rclone == nil {
		cfg.Rclone = &importer.Rclone{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		store: cfg.Store, queue: cfg.Queue, engine: cfg.Engine, video: cfg.Video, file: cfg.File, keyring: cfg.Keyring, log: cfg.Logger,
		metrics: cfg.Metrics, tmpDir: cfg.TmpDir, webhooks: cfg.Webhooks, newDriver: cfg.NewDriver,
		uploadTTL: cfg.UploadTTL, mpPartTTL: cfg.MultipartPartTTL, mpSessionTTL: cfg.MultipartSessionTTL,
		grace: cfg.SweepGrace, mpThreshold: cfg.MultipartThreshold, edgeBaseURL: cfg.EdgeBaseURL, fetcher: cfg.Fetcher, rclone: cfg.Rclone, partSize: cfg.PartSize, now: cfg.Now,
		drivers:     drivers{cache: map[string]cachedDriver{}},
		notifier:    newNotifier(),
		bytesIn:     cfg.Metrics.GetOrCreateCounter("kadr_bytes_in_total"),
		bytesStored: cfg.Metrics.GetOrCreateCounter("kadr_bytes_stored_total"),
		dedupHits:   cfg.Metrics.GetOrCreateCounter("kadr_dedup_hits_total"),
	}
}

// Register installs the pipeline job handlers on a runner.
func (s *Service) Register(r *queue.Runner) {
	r.Handle(JobProcessImage, queue.Handler{Run: s.runProcessImage, Dead: s.processImageDead, Lease: 2 * time.Minute})
	r.Handle(JobProcessFile, queue.Handler{Run: s.runProcessImage, Dead: s.processImageDead, Lease: 5 * time.Minute})
	r.Handle(JobProcessVideo, queue.Handler{Run: s.runProcessImage, Dead: s.processImageDead, Lease: 5 * time.Minute})
	r.Handle(JobSweepUploads, queue.Handler{Run: s.runSweepUploads, Lease: 5 * time.Minute})
	r.Handle(JobGCBlobs, queue.Handler{Run: s.runGCBlobs, Lease: 5 * time.Minute})
	r.Handle(JobGCBlob, queue.Handler{Run: s.runGCBlob})
	r.Handle(JobRetention, queue.Handler{Run: s.runRetention, Lease: 5 * time.Minute})
}

// Schedules returns the periodic maintenance jobs.
func Schedules() []queue.Schedule {
	return []queue.Schedule{
		{Kind: JobSweepUploads, Pool: queue.PoolMaintenance, Every: time.Minute},
		{Kind: JobGCBlobs, Pool: queue.PoolMaintenance, Every: 5 * time.Minute},
		{Kind: JobRetention, Pool: queue.PoolMaintenance, Every: time.Hour},
	}
}

// afterCommit wakes waiters of an asset and the webhook dispatcher.
func (s *Service) afterCommit(assetID string) {
	if assetID != "" {
		s.notifier.publish(assetID)
	}
	if s.webhooks != nil {
		s.webhooks.Notify()
	}
}

// emit writes an outbox event inside tx.
func (s *Service) emit(ctx context.Context, tx *db.Store, projectID, typ string, data any) error {
	_, err := webhook.Emit(ctx, tx, projectID, typ, data, s.now())
	return err
}

// ───────────── quota ─────────────

// reserve checks the project and namespace quotas for an additional bytes
// (and one more asset) and reserves the bytes. Must run inside a transaction.
func (s *Service) reserve(ctx context.Context, tx *db.Store, projectID, ns string, bytes int64) error {
	if err := s.checkQuota(ctx, tx, projectID, ns, bytes); err != nil {
		return err
	}
	return tx.AdjustUsage(ctx, projectID, ns, db.QuotaDelta{ReservedBytes: bytes})
}

// checkQuota returns 402 quota_exceeded when adding bytes would exceed a limit.
func (s *Service) checkQuota(ctx context.Context, tx *db.Store, projectID, ns string, bytes int64) error {
	p, err := tx.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if p.QuotaBytes != nil && p.UsedBytes+p.ReservedBytes+bytes > *p.QuotaBytes {
		return apiErr(http.StatusPaymentRequired, CodeQuotaExceeded,
			"project quota exceeded: %d of %d bytes used or reserved, %d more requested", p.UsedBytes+p.ReservedBytes, *p.QuotaBytes, bytes)
	}
	n, err := tx.EnsureNamespace(ctx, projectID, ns, s.now())
	if err != nil {
		return err
	}
	if n.QuotaBytes != nil && n.UsedBytes+n.ReservedBytes+bytes > *n.QuotaBytes {
		return apiErr(http.StatusPaymentRequired, CodeQuotaExceeded,
			"namespace %q quota exceeded: %d of %d bytes used or reserved, %d more requested", ns, n.UsedBytes+n.ReservedBytes, *n.QuotaBytes, bytes)
	}
	if n.QuotaAssets != nil && n.AssetCount+1 > *n.QuotaAssets {
		return apiErr(http.StatusPaymentRequired, CodeQuotaExceeded, "namespace %q asset limit of %d reached", ns, *n.QuotaAssets)
	}
	return nil
}

// Quota alert levels (namespaces.quota_alert_level).
const (
	alertNone     = 0
	alertWarning  = 1 // >= 80 %
	alertExceeded = 2 // >= 100 %
)

// updateQuotaAlerts emits quota.warning / quota.exceeded when the namespace
// usage crosses 80 % / 100 % upwards, once per crossing, and lowers the
// stored level when usage drops. Must run inside a transaction.
func (s *Service) updateQuotaAlerts(ctx context.Context, tx *db.Store, projectID, ns string) error {
	n, err := tx.GetNamespace(ctx, projectID, ns)
	if err != nil {
		return err
	}
	level := alertNone
	var ratio float64
	if n.QuotaBytes != nil && *n.QuotaBytes > 0 {
		ratio = float64(n.UsedBytes) / float64(*n.QuotaBytes)
		switch {
		case ratio >= 1:
			level = alertExceeded
		case ratio >= 0.8:
			level = alertWarning
		}
	}
	if level == n.QuotaAlertLevel {
		return nil
	}
	data := map[string]any{"namespace": ns, "used_bytes": n.UsedBytes, "quota_bytes": n.QuotaBytes,
		"ratio": float64(int(ratio*1000)) / 1000}
	if level >= alertWarning && n.QuotaAlertLevel < alertWarning {
		if err := s.emit(ctx, tx, projectID, webhook.EventQuotaWarning, data); err != nil {
			return err
		}
	}
	if level >= alertExceeded && n.QuotaAlertLevel < alertExceeded {
		if err := s.emit(ctx, tx, projectID, webhook.EventQuotaExceeded, data); err != nil {
			return err
		}
	}
	return tx.SetNamespaceAlertLevel(ctx, projectID, ns, level)
}

// ───────────── media types ─────────────

// mimeByFormat maps preset "accept" formats to allowed Content-Types.
var mimeByFormat = map[string][]string{
	"jpeg": {"image/jpeg", "image/jpg", "image/pjpeg"},
	"png":  {"image/png"},
	"webp": {"image/webp"},
	"heic": {"image/heic", "image/heif", "image/heic-sequence", "image/heif-sequence"},
	"avif": {"image/avif"},
	"gif":  {"image/gif"},
	"tiff": {"image/tiff"},
}

// normalizeContentType lowercases and strips parameters.
func normalizeContentType(ct string) string {
	ct, _, _ = strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// acceptsContentType reports whether ct is allowed by the accepted formats.
func acceptsContentType(accept []string, ct string) bool {
	for _, f := range accept {
		for _, m := range mimeByFormat[strings.ToLower(f)] {
			if m == ct {
				return true
			}
		}
	}
	return false
}

// renditionContentType returns the Content-Type of an encoded format.
func renditionContentType(format string) string {
	switch format {
	case "avif":
		return "image/avif"
	case "webp":
		return "image/webp"
	case "jpeg", "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "mp4":
		return "video/mp4"
	case "m3u8":
		return "application/vnd.apple.mpegurl"
	case "json":
		return "application/json; charset=utf-8"
	}
	if ct := file.ContentTypes[format]; ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func ptr[T any](v T) *T { return &v }
