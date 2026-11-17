package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Asset statuses (assets.status).
const (
	AssetProcessing = "processing"
	AssetReady      = "ready"
	AssetFailed     = "failed"
	AssetDeleted    = "deleted"
)

// Asset is the logical file a product refers to.
type Asset struct {
	ID            string
	ProjectID     string
	NS            string
	BlobID        *string
	UploadID      *string
	Kind          string
	Visibility    string
	Filename      *string
	Metadata      json.RawMessage
	Status        string
	ErrorCode     *string
	ErrorDetail   *string
	PresetID      *string
	PresetVersion *int64
	CreatedAt     time.Time
	ReadyAt       *time.Time
	DeletedAt     *time.Time
}

const assetCols = `id, project_id, ns, blob_id, upload_id, kind, visibility, filename, metadata_json, status,
	error_code, error_detail, preset_id, preset_version, created_at, ready_at, deleted_at`

func scanAsset(row interface{ Scan(...any) error }) (*Asset, error) {
	var a Asset
	var blobID, uploadID, filename, errCode, errDetail, presetID sql.NullString
	var presetVersion, ready, deleted sql.NullInt64
	var meta string
	var created int64
	if err := row.Scan(&a.ID, &a.ProjectID, &a.NS, &blobID, &uploadID, &a.Kind, &a.Visibility, &filename, &meta, &a.Status,
		&errCode, &errDetail, &presetID, &presetVersion, &created, &ready, &deleted); err != nil {
		return nil, mapErr(err)
	}
	a.BlobID, a.UploadID, a.Filename = fromNullStr(blobID), fromNullStr(uploadID), fromNullStr(filename)
	a.ErrorCode, a.ErrorDetail, a.PresetID = fromNullStr(errCode), fromNullStr(errDetail), fromNullStr(presetID)
	a.PresetVersion = fromNullInt(presetVersion)
	a.Metadata = json.RawMessage(meta)
	a.CreatedAt = fromMs(created)
	a.ReadyAt, a.DeletedAt = fromNullMs(ready), fromNullMs(deleted)
	return &a, nil
}

// CreateAsset inserts an asset.
func (s *Store) CreateAsset(ctx context.Context, a *Asset) error {
	meta := string(a.Metadata)
	if meta == "" {
		meta = "{}"
	}
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO assets (id, project_id, ns, blob_id, upload_id, kind, visibility, filename, metadata_json, status,
		  error_code, error_detail, preset_id, preset_version, created_at, ready_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.NS, nullStr(a.BlobID), nullStr(a.UploadID), a.Kind, a.Visibility, nullStr(a.Filename), meta,
		a.Status, nullStr(a.ErrorCode), nullStr(a.ErrorDetail), nullStr(a.PresetID), nullInt(a.PresetVersion),
		ms(a.CreatedAt), nullMs(a.ReadyAt), nullMs(a.DeletedAt))
	if err != nil {
		return fmt.Errorf("create asset: %w", mapErr(err))
	}
	return nil
}

// GetAsset returns an asset by id.
func (s *Store) GetAsset(ctx context.Context, id string) (*Asset, error) {
	return scanAsset(s.r.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets WHERE id = ?`, id))
}

// MarkAssetReady links a processing asset to its blob.
func (s *Store) MarkAssetReady(ctx context.Context, id, blobID string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE assets SET status = 'ready', blob_id = ?, ready_at = ?, error_code = NULL, error_detail = NULL
		 WHERE id = ? AND status = 'processing'`, blobID, ms(now), id)
}

// MarkAssetFailed records a processing failure.
func (s *Store) MarkAssetFailed(ctx context.Context, id, code, detail string) (bool, error) {
	return s.execOne(ctx,
		`UPDATE assets SET status = 'failed', error_code = ?, error_detail = ? WHERE id = ? AND status = 'processing'`,
		code, detail, id)
}

// MarkAssetDeleted soft-deletes an asset that is ready or failed.
func (s *Store) MarkAssetDeleted(ctx context.Context, id string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE assets SET status = 'deleted', deleted_at = ? WHERE id = ? AND status IN ('ready', 'failed')`, ms(now), id)
}

// SetAssetMetadata replaces metadata_json.
func (s *Store) SetAssetMetadata(ctx context.Context, id string, meta json.RawMessage) error {
	_, err := s.w.ExecContext(ctx, `UPDATE assets SET metadata_json = ? WHERE id = ?`, string(meta), id)
	return mapErr(err)
}

// AssetFilter selects assets for ListAssets.
type AssetFilter struct {
	ProjectID string
	NS        string // "" = all namespaces
	Kind      string // "" = all
	Status    string // "" = all except deleted
	// Cursor: return rows strictly after (CreatedBefore, IDBefore) in
	// (created_at DESC, id DESC) order. Zero = from the newest.
	CursorCreated int64
	CursorID      string
	Limit         int
}

// ListAssets returns assets newest first.
func (s *Store) ListAssets(ctx context.Context, f AssetFilter) ([]*Asset, error) {
	q := `SELECT ` + assetCols + ` FROM assets WHERE project_id = ?`
	args := []any{f.ProjectID}
	if f.NS != "" {
		q += ` AND ns = ?`
		args = append(args, f.NS)
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	} else {
		q += ` AND status != 'deleted'`
	}
	if f.CursorID != "" {
		q += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, f.CursorCreated, f.CursorCreated, f.CursorID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// AssetCreatedMs exposes the cursor component of an asset.
func AssetCreatedMs(a *Asset) int64 { return ms(a.CreatedAt) }

// ───────────── blobs and renditions ─────────────

// Blob statuses (blobs.status).
const (
	BlobProcessing = "processing"
	BlobReady      = "ready"
	BlobFailed     = "failed"
	BlobGCPending  = "gc_pending"
	BlobDeleted    = "deleted"
)

// Blob is the physical processing result shared by duplicate assets.
type Blob struct {
	ID                  string
	ProjectID           string
	StorageProfileID    string
	Visibility          string
	PresetID            string
	PresetVersion       int
	SourceSHA256        []byte
	Kind                string
	OriginalBytes       int64
	OriginalWidth       *int64
	OriginalHeight      *int64
	OriginalContentType *string
	OriginalKey         *string
	DurationMs          *int64
	ThumbHash           []byte
	DominantColor       *string
	TotalBytes          int64
	Refcount            int64
	Status              string
	CreatedAt           time.Time
}

// Rendition is one stored file of a blob.
type Rendition struct {
	BlobID     string
	Name       string
	Format     string
	StorageKey string
	Bytes      int64
	Width      *int64
	Height     *int64
	Bitrate    *int64
	Quality    *int64
	OverBudget bool
}

const blobCols = `id, project_id, storage_profile_id, visibility, preset_id, preset_version, source_sha256, kind,
	original_bytes, original_width, original_height, original_content_type, original_key, duration_ms, thumbhash,
	dominant_color, total_bytes, refcount, status, created_at`

func scanBlob(row interface{ Scan(...any) error }) (*Blob, error) {
	var b Blob
	var w, h, dur sql.NullInt64
	var ctype, okey, color sql.NullString
	var created int64
	if err := row.Scan(&b.ID, &b.ProjectID, &b.StorageProfileID, &b.Visibility, &b.PresetID, &b.PresetVersion,
		&b.SourceSHA256, &b.Kind, &b.OriginalBytes, &w, &h, &ctype, &okey, &dur, &b.ThumbHash, &color, &b.TotalBytes,
		&b.Refcount, &b.Status, &created); err != nil {
		return nil, mapErr(err)
	}
	b.OriginalWidth, b.OriginalHeight, b.DurationMs = fromNullInt(w), fromNullInt(h), fromNullInt(dur)
	b.OriginalContentType, b.OriginalKey, b.DominantColor = fromNullStr(ctype), fromNullStr(okey), fromNullStr(color)
	b.CreatedAt = fromMs(created)
	return &b, nil
}

// CreateBlob inserts a blob and its renditions. Returns ErrConflict when a
// blob with the same dedup key exists.
func (s *Store) CreateBlob(ctx context.Context, b *Blob, rs []*Rendition) error {
	return s.InTx(ctx, func(tx *Store) error {
		_, err := tx.w.ExecContext(ctx,
			`INSERT INTO blobs (id, project_id, storage_profile_id, visibility, preset_id, preset_version, source_sha256, kind,
			  original_bytes, original_width, original_height, original_content_type, original_key, duration_ms, thumbhash,
			  dominant_color, total_bytes, refcount, status, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.ProjectID, b.StorageProfileID, b.Visibility, b.PresetID, b.PresetVersion, b.SourceSHA256, b.Kind,
			b.OriginalBytes, nullInt(b.OriginalWidth), nullInt(b.OriginalHeight), nullStr(b.OriginalContentType),
			nullStr(b.OriginalKey), nullInt(b.DurationMs), b.ThumbHash, nullStr(b.DominantColor), b.TotalBytes, b.Refcount,
			b.Status, ms(b.CreatedAt))
		if err != nil {
			return fmt.Errorf("create blob: %w", mapErr(err))
		}
		for _, r := range rs {
			if _, err := tx.w.ExecContext(ctx,
				`INSERT INTO renditions (blob_id, name, format, storage_key, bytes, width, height, bitrate, quality, over_budget)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				b.ID, r.Name, r.Format, r.StorageKey, r.Bytes, nullInt(r.Width), nullInt(r.Height), nullInt(r.Bitrate),
				nullInt(r.Quality), r.OverBudget); err != nil {
				return fmt.Errorf("create rendition %s.%s: %w", r.Name, r.Format, mapErr(err))
			}
		}
		return nil
	})
}

// GetBlob returns a blob by id.
func (s *Store) GetBlob(ctx context.Context, id string) (*Blob, error) {
	return scanBlob(s.r.QueryRowContext(ctx, `SELECT `+blobCols+` FROM blobs WHERE id = ?`, id))
}

// FindReadyBlob looks up a ready blob by its dedup key.
func (s *Store) FindReadyBlob(ctx context.Context, projectID, visibility, presetID string, presetVersion int, sha []byte) (*Blob, error) {
	return scanBlob(s.r.QueryRowContext(ctx,
		`SELECT `+blobCols+` FROM blobs WHERE project_id = ? AND visibility = ? AND preset_id = ? AND preset_version = ?
		   AND source_sha256 = ? AND status = 'ready'`, projectID, visibility, presetID, presetVersion, sha))
}

// AddBlobRef increments the refcount of a ready blob. Reports false when the
// blob is no longer ready.
func (s *Store) AddBlobRef(ctx context.Context, id string) (bool, error) {
	return s.execOne(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ? AND status = 'ready'`, id)
}

// ReleaseBlobRef decrements the refcount. When it reaches zero the blob
// becomes gc_pending and its dedup key is freed: the UNIQUE constraint on
// (project, visibility, preset, version, source_sha256) covers every status,
// so the hash is suffixed with the blob id ("tombstoned") to let the same
// content be processed again while GC is still pending. Returns the blob
// after the update.
func (s *Store) ReleaseBlobRef(ctx context.Context, id string) (*Blob, error) {
	return scanBlob(s.w.QueryRowContext(ctx,
		`UPDATE blobs SET
		   refcount = max(0, refcount - 1),
		   status = CASE WHEN refcount <= 1 THEN 'gc_pending' ELSE status END,
		   source_sha256 = CASE WHEN refcount <= 1 THEN source_sha256 || CAST(id AS BLOB) ELSE source_sha256 END
		 WHERE id = ? RETURNING `+blobCols, id))
}

// ListGCPendingBlobs returns blobs waiting for object deletion.
func (s *Store) ListGCPendingBlobs(ctx context.Context, limit int) ([]*Blob, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+blobCols+` FROM blobs WHERE status = 'gc_pending' ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Blob{}
	for rows.Next() {
		b, err := scanBlob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, mapErr(rows.Err())
}

// MarkBlobDeleted finishes GC of a blob that is still gc_pending.
func (s *Store) MarkBlobDeleted(ctx context.Context, id string) (bool, error) {
	return s.execOne(ctx, `UPDATE blobs SET status = 'deleted' WHERE id = ? AND status = 'gc_pending' AND refcount = 0`, id)
}

// ListRenditions returns the renditions of a blob in a stable order.
func (s *Store) ListRenditions(ctx context.Context, blobID string) ([]*Rendition, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT blob_id, name, format, storage_key, bytes, width, height, bitrate, quality, over_budget
		 FROM renditions WHERE blob_id = ? ORDER BY rowid`, blobID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Rendition{}
	for rows.Next() {
		var r Rendition
		var w, h, br, q sql.NullInt64
		if err := rows.Scan(&r.BlobID, &r.Name, &r.Format, &r.StorageKey, &r.Bytes, &w, &h, &br, &q, &r.OverBudget); err != nil {
			return nil, mapErr(err)
		}
		r.Width, r.Height, r.Bitrate, r.Quality = fromNullInt(w), fromNullInt(h), fromNullInt(br), fromNullInt(q)
		out = append(out, &r)
	}
	return out, mapErr(rows.Err())
}
