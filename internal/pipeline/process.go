package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/image"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/video"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

type processPayload struct {
	UploadID string `json:"upload_id"`
}

// processCtx is everything one process_image run needs.
type processCtx struct {
	job     *queue.Job
	upload  *db.Upload
	asset   *db.Asset
	project *db.Project
	kind    string // image | video
	params  preset.ImageParams
	vparams preset.VideoParams
	tgt     *target
	workDir string
}

// permanent wraps a classified failure so the queue does not retry it; the
// Dead handler then marks the asset failed with code.
func permanent(code, detail string, err error) error {
	return queue.Permanent(&jobError{code: code, detail: detail, err: err})
}

// runProcessImage is the process_image job: download → hash → dedup →
// probe → process → upload renditions → commit blob + asset + quota +
// usage + event in one transaction.
func (s *Service) runProcessImage(ctx context.Context, j *queue.Job) error {
	var p processPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.UploadID == "" {
		return queue.Permanent(fmt.Errorf("bad payload %s", j.Payload))
	}
	u, err := s.store.GetUpload(ctx, p.UploadID)
	if err != nil {
		return queue.Permanent(fmt.Errorf("load upload %s: %w", p.UploadID, err))
	}
	if u.AssetID == nil {
		return queue.Permanent(fmt.Errorf("upload %s has no asset", u.ID))
	}
	a, err := s.store.GetAsset(ctx, *u.AssetID)
	if err != nil {
		return err
	}
	if a.Status != db.AssetProcessing {
		return nil // already settled by an earlier attempt
	}
	if _, err := s.store.TransitionUpload(ctx, u.ID, []string{db.UploadUploaded}, db.UploadProcessing, nil, s.now()); err != nil {
		return err
	}
	pc := &processCtx{job: j, upload: u, asset: a}
	if pc.project, err = s.store.GetProject(ctx, u.ProjectID); err != nil {
		return err
	}
	pv, err := s.store.GetPresetVersion(ctx, u.PresetID, u.PresetVersion)
	if err != nil {
		return permanent(FailInternal, "preset version not found", err)
	}
	pc.kind = pv.Kind
	if pc.kind == "video" {
		err = json.Unmarshal(pv.Params, &pc.vparams)
	} else {
		pc.kind = "image"
		err = json.Unmarshal(pv.Params, &pc.params)
	}
	if err != nil {
		return permanent(FailInternal, "invalid preset parameters", err)
	}
	if pc.tgt, err = s.storageFor(ctx, pc.project); err != nil {
		return err
	}
	pc.workDir = filepath.Join(s.tmpDir, j.ID)
	if err := os.MkdirAll(pc.workDir, 0o750); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(pc.workDir); err != nil {
			s.log.Warn("remove work dir", "dir", pc.workDir, "err", err)
		}
	}()

	srcPath, size, sha, err := s.fetchSource(ctx, pc)
	if err != nil {
		return err
	}
	if u.DeclaredSHA256 != nil && !bytes.Equal(u.DeclaredSHA256, sha) {
		return permanent(FailChecksumMismatch, "the uploaded file does not match the declared sha256", nil)
	}
	s.bytesIn.Add(int(size))

	// Dedup again now that the real hash is known.
	if b, err := s.store.FindReadyBlob(ctx, u.ProjectID, u.Visibility, u.PresetID, u.PresetVersion, sha); err == nil {
		return s.finishWithExistingBlob(ctx, pc, b, size)
	} else if !errors.Is(err, db.ErrNotFound) {
		return err
	}

	outDir := filepath.Join(pc.workDir, "out")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	var out *produced
	if pc.kind == "video" {
		out, err = s.produceVideo(ctx, pc, srcPath, outDir)
	} else {
		out, err = s.produceImage(ctx, pc, srcPath, outDir)
	}
	if err != nil {
		return err
	}

	blob := &db.Blob{
		ID: db.NewID(db.PrefixBlob), ProjectID: u.ProjectID, StorageProfileID: pc.tgt.profile.ID, Visibility: u.Visibility,
		PresetID: u.PresetID, PresetVersion: u.PresetVersion, SourceSHA256: sha, Kind: pc.kind, OriginalBytes: size,
		OriginalWidth: ptr(int64(out.srcWidth)), OriginalHeight: ptr(int64(out.srcHeight)), OriginalContentType: &out.srcContentType,
		ThumbHash: out.thumbHash, Refcount: 1, Status: db.BlobReady, CreatedAt: s.now(),
	}
	if out.durationMs > 0 {
		blob.DurationMs = &out.durationMs
	}
	if out.dominantColor != "" {
		blob.DominantColor = &out.dominantColor
	}
	bucket := pc.tgt.bucket(u.Visibility)
	cacheControl := CacheControlPrivate
	if u.Visibility == "public" {
		cacheControl = CacheControlPublic
	}
	var rends []*db.Rendition
	var uploaded []string // keys, for cleanup on failure
	cleanup := func() {
		for _, k := range uploaded {
			_ = pc.tgt.driver.Delete(context.WithoutCancel(ctx), bucket, k)
		}
		if blob.OriginalKey != nil {
			_ = pc.tgt.driver.Delete(context.WithoutCancel(ctx), pc.tgt.profile.PrivateBucket, *blob.OriginalKey)
		}
	}
	for _, r := range out.files {
		key := fmt.Sprintf("%s/%s/%s.%s", pc.project.Slug, blob.ID, r.name, r.format)
		if err := putFile(ctx, pc.tgt.driver, bucket, key, r.path, storage.PutOptions{
			ContentType: renditionContentType(r.format), CacheControl: cacheControl,
		}); err != nil {
			cleanup()
			return fmt.Errorf("upload rendition %s.%s: %w", r.name, r.format, err)
		}
		uploaded = append(uploaded, key)
		rd := &db.Rendition{BlobID: blob.ID, Name: r.name, Format: r.format, StorageKey: key, Bytes: r.bytes,
			Width: ptr(int64(r.width)), Height: ptr(int64(r.height)), OverBudget: r.overBudget}
		if r.quality > 0 {
			rd.Quality = ptr(int64(r.quality))
		}
		if r.bitrate > 0 {
			rd.Bitrate = ptr(r.bitrate)
		}
		rends = append(rends, rd)
		blob.TotalBytes += r.bytes
	}
	if out.retainOrigin {
		key := fmt.Sprintf("originals/%s/%s.%s", pc.project.Slug, blob.ID, out.srcFormat)
		if err := putFile(ctx, pc.tgt.driver, pc.tgt.profile.PrivateBucket, key, srcPath, storage.PutOptions{
			ContentType: out.srcContentType, CacheControl: CacheControlPrivate,
		}); err != nil {
			cleanup()
			return fmt.Errorf("upload original: %w", err)
		}
		blob.OriginalKey = &key
	}

	var view *AssetView
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateBlob(ctx, blob, rends); err != nil {
			return err
		}
		ok, err := tx.MarkAssetReady(ctx, a.ID, blob.ID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return errAssetSettled
		}
		if err := s.settleReady(ctx, tx, pc, blob.TotalBytes, db.UsageDelta{
			AssetsReady: 1, BytesIn: size, BytesStored: blob.TotalBytes, CPUMillis: out.cpuMillis, VideoSec: out.durationMs / 1000,
		}); err != nil {
			return err
		}
		view, err = s.readyEvent(ctx, tx, a.ID)
		return err
	})
	switch {
	case errors.Is(err, db.ErrConflict):
		// Another job stored the same content meanwhile: reuse its blob.
		cleanup()
		b, ferr := s.store.FindReadyBlob(ctx, u.ProjectID, u.Visibility, u.PresetID, u.PresetVersion, sha)
		if ferr != nil {
			return fmt.Errorf("blob conflict and no ready blob: %w", ferr)
		}
		return s.finishWithExistingBlob(ctx, pc, b, size)
	case errors.Is(err, errAssetSettled):
		cleanup()
		return nil
	case err != nil:
		cleanup()
		return err
	}
	s.bytesStored.Add(int(blob.TotalBytes))
	s.removeSource(ctx, pc)
	s.afterCommit(view.ID)
	return nil
}

var errAssetSettled = errors.New("asset is no longer processing")

// settleReady finishes the upload, settles quota (reserved → used), records
// usage and quota alerts. Must run inside the finalizing transaction.
func (s *Service) settleReady(ctx context.Context, tx *db.Store, pc *processCtx, storedBytes int64, usage db.UsageDelta) error {
	u := pc.upload
	d := db.QuotaDelta{UsedBytes: storedBytes, AssetCount: 1}
	ok, err := tx.TransitionUpload(ctx, u.ID, []string{db.UploadUploaded, db.UploadProcessing}, db.UploadDone, nil, s.now())
	if err != nil {
		return err
	}
	if ok {
		d.ReservedBytes = -u.DeclaredBytes
	}
	if err := tx.AdjustUsage(ctx, u.ProjectID, u.NS, d); err != nil {
		return err
	}
	if err := tx.AddUsageDaily(ctx, u.ProjectID, u.NS, s.now(), usage); err != nil {
		return err
	}
	return s.updateQuotaAlerts(ctx, tx, u.ProjectID, u.NS)
}

func (s *Service) readyEvent(ctx context.Context, tx *db.Store, assetID string) (*AssetView, error) {
	a, err := tx.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	view, err := s.assetView(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	return view, s.emit(ctx, tx, a.ProjectID, webhook.EventAssetReady, map[string]any{"asset": view, "metadata": view.Metadata})
}

// finishWithExistingBlob links the asset to an already stored blob.
func (s *Service) finishWithExistingBlob(ctx context.Context, pc *processCtx, b *db.Blob, size int64) error {
	var view *AssetView
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		ok, err := tx.AddBlobRef(ctx, b.ID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("blob %s is no longer ready", b.ID) // retried; dedup will miss next time
		}
		if ok, err = tx.MarkAssetReady(ctx, pc.asset.ID, b.ID, s.now()); err != nil {
			return err
		} else if !ok {
			return errAssetSettled
		}
		if err := s.settleReady(ctx, tx, pc, b.TotalBytes, db.UsageDelta{AssetsReady: 1, BytesIn: size, DedupHits: 1}); err != nil {
			return err
		}
		view, err = s.readyEvent(ctx, tx, pc.asset.ID)
		return err
	})
	if errors.Is(err, errAssetSettled) {
		return nil
	}
	if err != nil {
		return err
	}
	s.dedupHits.Inc()
	s.removeSource(ctx, pc)
	s.afterCommit(view.ID)
	return nil
}

// fetchSource returns a local path to the source and its size and SHA-256.
// Ticket uploads are streamed from the tmp object to <workDir>/source;
// server-side uploads are read from their staged file.
func (s *Service) fetchSource(ctx context.Context, pc *processCtx) (string, int64, []byte, error) {
	u := pc.upload
	if u.Method == db.UploadMethodServer {
		path := s.stagedPath(deref(u.SourceRef))
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, nil, permanent(FailSourceMissing, "the staged file of this server-side upload is gone", err)
		}
		if err != nil {
			return "", 0, nil, err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return "", 0, nil, err
		}
		return path, n, h.Sum(nil), nil
	}
	body, _, err := pc.tgt.driver.Get(ctx, pc.tgt.profile.PrivateBucket, deref(u.TmpKey))
	if errors.Is(err, storage.ErrNotFound) {
		return "", 0, nil, permanent(FailSourceMissing, "the uploaded file is no longer in temporary storage", err)
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("download source: %w", err)
	}
	defer body.Close()
	path := filepath.Join(pc.workDir, "source")
	n, sha, err := writeHashed(path, body, u.DeclaredBytes)
	if errors.Is(err, errTooLarge) {
		return "", 0, nil, permanent(image.CodeTooLarge, "the stored file is larger than declared", nil)
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("download source: %w", err)
	}
	return path, n, sha, nil
}

func (s *Service) stagedPath(ref string) string {
	return filepath.Join(s.tmpDir, "staged", filepath.Base(ref))
}

// removeSource deletes the tmp object or staged file once the asset settled.
func (s *Service) removeSource(ctx context.Context, pc *processCtx) {
	u := pc.upload
	if u.Method == db.UploadMethodServer {
		if err := os.Remove(s.stagedPath(deref(u.SourceRef))); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("remove staged upload", "upload_id", u.ID, "err", err)
		}
		return
	}
	if pc.tgt == nil || u.TmpKey == nil {
		return
	}
	if err := pc.tgt.driver.Delete(context.WithoutCancel(ctx), pc.tgt.profile.PrivateBucket, *u.TmpKey); err != nil {
		s.log.Warn("delete tmp object", "upload_id", u.ID, "err", err)
	}
}

// classifyEngineError maps engine errors: non-retryable → permanent failure
// with the engine code; retryable → plain error (queue retry).
func classifyEngineError(err error) error {
	if ie, ok := image.AsError(err); ok {
		if ie.Retryable {
			return &jobError{code: ie.Code, detail: ie.Detail, err: err}
		}
		return permanent(ie.Code, ie.Detail, err)
	}
	if ve, ok := video.AsError(err); ok {
		if ve.Retryable {
			return &jobError{code: ve.Code, detail: ve.Detail, err: err}
		}
		return permanent(ve.Code, ve.Detail, err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &jobError{code: image.CodeEngineFailure, detail: "unclassified engine error", err: err}
}

// processImageDead settles an asset whose job ended in the dead state:
// asset failed + asset.failed event + reservation released.
func (s *Service) processImageDead(ctx context.Context, j *queue.Job, cause error) {
	var p processPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return
	}
	u, err := s.store.GetUpload(ctx, p.UploadID)
	if err != nil || u.AssetID == nil {
		return
	}
	code, detail := FailInternal, "processing failed"
	var je *jobError
	if errors.As(cause, &je) {
		code, detail = je.code, je.detail
	}
	if err := s.failAsset(ctx, u, code, detail); err != nil {
		s.log.Error("mark asset failed", "asset_id", *u.AssetID, "err", err)
	}
}

// failAsset marks the asset of u failed, releases the reservation, emits
// asset.failed and removes the source.
func (s *Service) failAsset(ctx context.Context, u *db.Upload, code, detail string) error {
	var changed bool
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		ok, err := tx.MarkAssetFailed(ctx, *u.AssetID, code, detail)
		if err != nil || !ok {
			return err
		}
		changed = true
		moved, err := tx.TransitionUpload(ctx, u.ID, []string{db.UploadUploaded, db.UploadProcessing}, db.UploadFailed, nil, s.now())
		if err != nil {
			return err
		}
		if moved {
			if err := tx.AdjustUsage(ctx, u.ProjectID, u.NS, db.QuotaDelta{ReservedBytes: -u.DeclaredBytes}); err != nil {
				return err
			}
		}
		if err := tx.AddUsageDaily(ctx, u.ProjectID, u.NS, s.now(), db.UsageDelta{AssetsFailed: 1}); err != nil {
			return err
		}
		a, err := tx.GetAsset(ctx, *u.AssetID)
		if err != nil {
			return err
		}
		view, err := s.assetView(ctx, tx, a)
		if err != nil {
			return err
		}
		return s.emit(ctx, tx, u.ProjectID, webhook.EventAssetFailed, map[string]any{
			"asset": view, "metadata": view.Metadata, "error": AssetError{Code: code, Detail: detail},
		})
	})
	if err != nil || !changed {
		return err
	}
	pc := &processCtx{upload: u}
	if u.Method != db.UploadMethodServer {
		if project, err := s.store.GetProject(ctx, u.ProjectID); err == nil {
			pc.tgt, _ = s.storageFor(ctx, project)
		}
	}
	s.removeSource(ctx, pc)
	s.afterCommit(*u.AssetID)
	return nil
}

// putFile uploads a local file.
func putFile(ctx context.Context, d storage.Driver, bucket, key, path string, opts storage.PutOptions) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	return d.Put(ctx, bucket, key, f, st.Size(), opts)
}
