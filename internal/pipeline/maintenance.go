package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

// Retention periods (docs/DATA_MODEL.md).
const (
	RetainEvents      = 30 * 24 * time.Hour
	RetainDeliveries  = 30 * 24 * time.Hour
	RetainJobs        = 7 * 24 * time.Hour
	RetainDeadJobs    = 30 * 24 * time.Hour
	RetainIdempotency = 24 * time.Hour
	RetainAudit       = 365 * 24 * time.Hour
)

const maintenanceBatch = 100

type gcPayload struct {
	BlobID string `json:"blob_id"`
}

// runSweepUploads handles pending uploads whose ticket expired: if the
// object exists it is processed (the client never called complete),
// otherwise the upload expires, upload.expired is emitted and the
// reservation is released.
//
// Per-upload errors are logged and retried on the next tick, so a single
// broken upload never turns the periodic job into a dead letter.
func (s *Service) runSweepUploads(ctx context.Context, _ *queue.Job) error {
	res, err := s.SweepExpiredUploads(ctx)
	if err != nil {
		s.log.Warn("upload sweep finished with errors", "err", err, "promoted", res.Promoted, "expired", res.Expired)
	}
	return nil
}

// SweepResult counts what a sweep did.
type SweepResult struct{ Promoted, Expired, Failed int }

// SweepExpiredUploads runs one sweep pass.
func (s *Service) SweepExpiredUploads(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	ups, err := s.store.ListExpiredPendingUploads(ctx, s.now().Add(-s.grace), maintenanceBatch)
	if err != nil {
		return res, err
	}
	var errs []error
	for _, u := range ups {
		_, err := s.promote(ctx, u, false)
		var ae *Error
		switch {
		case err == nil:
			res.Promoted++
			s.log.Info("sweeper started processing an upload without complete", "upload_id", u.ID)
		case errors.Is(err, storage.ErrNotFound):
			if err := s.expireUpload(ctx, u); err != nil {
				errs = append(errs, err)
			} else {
				res.Expired++
			}
		case errors.As(err, &ae) && ae.Code == CodeSizeMismatch:
			res.Failed++
		default:
			errs = append(errs, fmt.Errorf("upload %s: %w", u.ID, err))
		}
	}
	return res, errors.Join(errs...)
}

func (s *Service) expireUpload(ctx context.Context, u *db.Upload) error {
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		ok, err := tx.TransitionUpload(ctx, u.ID, []string{db.UploadPending}, db.UploadExpired, nil, s.now())
		if err != nil || !ok {
			return err
		}
		if err := tx.AdjustUsage(ctx, u.ProjectID, u.NS, db.QuotaDelta{ReservedBytes: -u.DeclaredBytes}); err != nil {
			return err
		}
		slug := ""
		if pv, err := tx.GetPresetVersion(ctx, u.PresetID, u.PresetVersion); err == nil {
			slug = pv.Slug
		}
		return s.emit(ctx, tx, u.ProjectID, webhook.EventUploadExpired, map[string]any{
			"upload_id": u.ID, "namespace": u.NS, "preset": slug, "visibility": u.Visibility,
			"filename": u.Filename, "expires_at": rfc3339(u.ExpiresAt), "metadata": u.Metadata,
		})
	})
	if err == nil {
		s.afterCommit("")
	}
	return err
}

// runGCBlobs is the periodic GC pass; failures are retried on the next tick.
func (s *Service) runGCBlobs(ctx context.Context, _ *queue.Job) error {
	if _, err := s.CollectGarbage(ctx); err != nil {
		s.log.Warn("blob GC finished with errors", "err", err)
	}
	return nil
}

// CollectGarbage deletes objects of every gc_pending blob (one batch).
func (s *Service) CollectGarbage(ctx context.Context) (int, error) {
	blobs, err := s.store.ListGCPendingBlobs(ctx, maintenanceBatch)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, b := range blobs {
		if err := s.gcBlob(ctx, b); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

func (s *Service) runGCBlob(ctx context.Context, j *queue.Job) error {
	var p gcPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.BlobID == "" {
		return queue.Permanent(fmt.Errorf("bad payload %s", j.Payload))
	}
	b, err := s.store.GetBlob(ctx, p.BlobID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.Status != db.BlobGCPending {
		return nil
	}
	return s.gcBlob(ctx, b)
}

// gcBlob deletes the objects of a blob, then marks it deleted.
func (s *Service) gcBlob(ctx context.Context, b *db.Blob) error {
	tgt, err := s.storageByProfile(ctx, b.StorageProfileID)
	if err != nil {
		return fmt.Errorf("gc blob %s: %w", b.ID, err)
	}
	rs, err := s.store.ListRenditions(ctx, b.ID)
	if err != nil {
		return err
	}
	bucket := tgt.bucket(b.Visibility)
	for _, r := range rs {
		if err := tgt.driver.Delete(ctx, bucket, r.StorageKey); err != nil {
			return fmt.Errorf("gc blob %s: %w", b.ID, err)
		}
	}
	if b.OriginalKey != nil {
		if err := tgt.driver.Delete(ctx, tgt.profile.PrivateBucket, *b.OriginalKey); err != nil {
			return fmt.Errorf("gc blob %s original: %w", b.ID, err)
		}
	}
	if _, err := s.store.MarkBlobDeleted(ctx, b.ID); err != nil {
		return err
	}
	s.log.Info("blob garbage collected", "blob_id", b.ID, "objects", len(rs))
	return nil
}

func (s *Service) runRetention(ctx context.Context, _ *queue.Job) error {
	now := s.now()
	r, err := s.store.PurgeExpired(ctx, db.RetentionCutoffs{
		Events: now.Add(-RetainEvents), Deliveries: now.Add(-RetainDeliveries), Jobs: now.Add(-RetainJobs), DeadJobs: now.Add(-RetainDeadJobs),
		IdempotencyKey: now.Add(-RetainIdempotency), AuditLog: now.Add(-RetainAudit),
	})
	if err != nil {
		return err
	}
	s.log.Info("retention cleanup", "events", r.Events, "deliveries", r.Deliveries, "jobs", r.Jobs,
		"idempotency_keys", r.IdempotencyKeys, "audit_log", r.AuditLog)
	return nil
}

// CleanStaleWorkDirs removes leftovers in TmpDir at startup (before the
// runner starts): every per-job work directory ("job_*") and staged
// server-side uploads that no active upload references.
func (s *Service) CleanStaleWorkDirs(ctx context.Context) error {
	entries, err := os.ReadDir(s.tmpDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), db.PrefixJob+"_") {
			if err := os.RemoveAll(filepath.Join(s.tmpDir, e.Name())); err != nil {
				s.log.Warn("remove stale work dir", "dir", e.Name(), "err", err)
				continue
			}
			removed++
		}
	}
	active, err := s.store.ListActiveServerUploadRefs(ctx)
	if err != nil {
		return err
	}
	staged, err := os.ReadDir(filepath.Join(s.tmpDir, "staged"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range staged {
		if !active[e.Name()] {
			if err := os.RemoveAll(filepath.Join(s.tmpDir, "staged", e.Name())); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		s.log.Info("removed stale temporary files", "count", removed)
	}
	return nil
}
