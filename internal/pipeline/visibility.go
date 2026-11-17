package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

// changeVisibility moves a ready asset between the public and the private
// bucket. Blobs are immutable and shared by dedup, so the renditions are
// copied (server-side) into a new blob of the target visibility, the asset is
// re-pointed to it and the old blob loses a reference (GC removes the old
// objects once nobody uses them). The metadata, when given, is applied in the
// same transaction.
func (s *Service) changeVisibility(ctx context.Context, a *db.Asset, to string, rawMeta []byte) (*AssetView, error) {
	if to != "public" && to != "private" {
		return nil, validationErr(map[string]string{"visibility": `must be "public" or "private"`})
	}
	if a.Status != db.AssetReady || a.BlobID == nil {
		return nil, apiErr(http.StatusConflict, "asset_not_ready", "asset %s is %s; visibility can only be changed on a ready asset", a.ID, a.Status)
	}
	var meta []byte
	if rawMeta != nil {
		m, msg := normalizeMetadata(rawMeta)
		if msg != "" {
			return nil, validationErr(map[string]string{"metadata": msg})
		}
		meta = m
	}
	old, err := s.store.GetBlob(ctx, *a.BlobID)
	if err != nil {
		return nil, err
	}
	project, err := s.store.GetProject(ctx, a.ProjectID)
	if err != nil {
		return nil, err
	}
	tgt, err := s.storageByProfile(ctx, old.StorageProfileID)
	if err != nil {
		return nil, err
	}
	rs, err := s.store.ListRenditions(ctx, old.ID)
	if err != nil {
		return nil, err
	}

	// Reuse an equivalent blob that already exists in the target visibility.
	target, ferr := s.store.FindReadyBlob(ctx, old.ProjectID, to, old.PresetID, old.PresetVersion, old.SourceSHA256)
	if ferr != nil && !errors.Is(ferr, db.ErrNotFound) {
		return nil, ferr
	}
	var newBlob *db.Blob
	var newRends []*db.Rendition
	var copied []string
	srcBucket, dstBucket := tgt.bucket(old.Visibility), tgt.bucket(to)
	cache := CacheControlPrivate
	if to == "public" {
		cache = CacheControlPublic
	}
	cleanup := func() {
		for _, k := range copied {
			_ = tgt.driver.Delete(context.WithoutCancel(ctx), dstBucket, k)
		}
	}
	if target == nil {
		nb := *old
		nb.ID, nb.Visibility, nb.Refcount, nb.Status, nb.CreatedAt = db.NewID(db.PrefixBlob), to, 1, db.BlobReady, s.now()
		newBlob = &nb
		for _, r := range rs {
			nr := *r
			nr.BlobID = nb.ID
			nr.StorageKey = strings.Replace(r.StorageKey, old.ID, nb.ID, 1)
			if r.Format == hlsRenditionFormat { // the master row stands for the whole package
				keys, err := s.copyHLSPackage(ctx, tgt.driver, srcBucket, dstBucket, r.StorageKey, old.ID, nb.ID, cache)
				copied = append(copied, keys...)
				if err != nil {
					cleanup()
					return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not copy the HLS package: %v", err)
				}
				newRends = append(newRends, &nr)
				continue
			}
			po := storage.PutOptions{ContentType: renditionContentType(r.Format), CacheControl: cache}
			if old.Kind == "file" && r.Name == "main" {
				po.ContentDisposition = contentDispositionFor(deref(a.Filename), r.Format)
			}
			if err := tgt.driver.Copy(ctx, srcBucket, r.StorageKey, dstBucket, nr.StorageKey, po); err != nil {
				cleanup()
				return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not copy %s: %v", r.StorageKey, err)
			}
			copied = append(copied, nr.StorageKey)
			newRends = append(newRends, &nr)
		}
		if old.OriginalKey != nil { // originals always live in the private bucket
			k := strings.Replace(*old.OriginalKey, old.ID, nb.ID, 1)
			if err := tgt.driver.Copy(ctx, tgt.profile.PrivateBucket, *old.OriginalKey, tgt.profile.PrivateBucket, k); err != nil {
				cleanup()
				return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not copy the original: %v", err)
			}
			nb.OriginalKey = &k
		}
	}

	var view *AssetView
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		blobID := ""
		if target != nil {
			ok, err := tx.AddBlobRef(ctx, target.ID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("blob %s is no longer ready", target.ID)
			}
			blobID = target.ID
		} else {
			if err := tx.CreateBlob(ctx, newBlob, newRends); err != nil {
				return err
			}
			blobID = newBlob.ID
		}
		moved, err := tx.MoveAssetToBlob(ctx, a.ID, blobID, to)
		if err != nil {
			return err
		}
		if !moved {
			return apiErr(http.StatusConflict, "asset_not_ready", "asset %s changed concurrently", a.ID)
		}
		ob, err := tx.ReleaseBlobRef(ctx, old.ID)
		if err != nil {
			return err
		}
		if ob.Status == db.BlobGCPending {
			if _, _, err := s.queue.Enqueue(ctx, tx, queue.EnqueueOptions{
				Kind: JobGCBlob, Pool: queue.PoolMaintenance, DedupeKey: "gc:" + ob.ID, Payload: gcPayload{BlobID: ob.ID},
			}); err != nil {
				return err
			}
		}
		changes := []string{"visibility"}
		if meta != nil {
			if err := tx.SetAssetMetadata(ctx, a.ID, meta); err != nil {
				return err
			}
			changes = append(changes, "metadata")
		}
		cur, err := tx.GetAsset(ctx, a.ID)
		if err != nil {
			return err
		}
		if view, err = s.assetView(ctx, tx, cur); err != nil {
			return err
		}
		return s.emit(ctx, tx, project.ID, webhook.EventAssetUpdated, map[string]any{
			"asset": view, "metadata": view.Metadata, "changes": changes,
		})
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	s.afterCommit("")
	return view, nil
}
