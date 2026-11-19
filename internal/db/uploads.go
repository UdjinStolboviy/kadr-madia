package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ID prefixes of the media aggregates.
const (
	PrefixUpload = "upl"
	PrefixAsset  = "ast"
	PrefixBlob   = "blb"
)

// Upload statuses (uploads.status). The pipeline state machine:
//
//	pending → uploaded → processing → done | failed
//	pending → expired | aborted
const (
	UploadPending    = "pending"
	UploadUploaded   = "uploaded"
	UploadProcessing = "processing"
	UploadDone       = "done"
	UploadExpired    = "expired"
	UploadAborted    = "aborted"
	UploadFailed     = "failed"
)

// Upload methods (uploads.method).
const (
	UploadMethodPut       = "PUT"
	UploadMethodMultipart = "MULTIPART"
	UploadMethodServer    = "SERVER"
	UploadMethodURL       = "URL"
	UploadMethodCloud     = "CLOUD"
)

// Upload is an upload ticket and the source of an asset.
type Upload struct {
	ID             string
	ProjectID      string
	NS             string
	PresetID       string
	PresetVersion  int
	Visibility     string
	Method         string
	TmpKey         *string
	MultipartID    *string // S3 multipart upload id (method MULTIPART)
	PartSize       int64   // byte size per part (method MULTIPART; 0 if unset)
	Filename       *string
	ContentType    *string
	DeclaredBytes  int64
	DeclaredSHA256 []byte // nil when not declared
	SourceRef      *string
	Metadata       json.RawMessage
	Status         string
	AssetID        *string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Active reports whether the upload still holds a quota reservation.
func (u *Upload) Active() bool {
	switch u.Status {
	case UploadPending, UploadUploaded, UploadProcessing:
		return true
	}
	return false
}

const uploadCols = `id, project_id, ns, preset_id, preset_version, visibility, method, tmp_key, multipart_id, part_size, filename, content_type,
	declared_bytes, declared_sha256, source_ref, metadata_json, status, asset_id, expires_at, created_at, updated_at`

func scanUpload(row interface{ Scan(...any) error }) (*Upload, error) {
	var u Upload
	var tmpKey, mpID, filename, ctype, sourceRef, assetID sql.NullString
	var partSize, declared sql.NullInt64
	var meta string
	var expires, created, updated int64
	if err := row.Scan(&u.ID, &u.ProjectID, &u.NS, &u.PresetID, &u.PresetVersion, &u.Visibility, &u.Method, &tmpKey, &mpID, &partSize,
		&filename, &ctype, &declared, &u.DeclaredSHA256, &sourceRef, &meta, &u.Status, &assetID, &expires, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	u.TmpKey, u.Filename, u.ContentType = fromNullStr(tmpKey), fromNullStr(filename), fromNullStr(ctype)
	u.MultipartID = fromNullStr(mpID)
	u.PartSize = partSize.Int64
	u.SourceRef, u.AssetID = fromNullStr(sourceRef), fromNullStr(assetID)
	u.DeclaredBytes = declared.Int64
	u.Metadata = json.RawMessage(meta)
	u.ExpiresAt, u.CreatedAt, u.UpdatedAt = fromMs(expires), fromMs(created), fromMs(updated)
	return &u, nil
}

// CreateUpload inserts an upload.
func (s *Store) CreateUpload(ctx context.Context, u *Upload) error {
	meta := string(u.Metadata)
	if meta == "" {
		meta = "{}"
	}
	partSize := sql.NullInt64{Int64: u.PartSize, Valid: u.PartSize > 0}
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO uploads (id, project_id, ns, preset_id, preset_version, visibility, method, tmp_key, multipart_id, part_size, filename,
		  content_type, declared_bytes, declared_sha256, source_ref, metadata_json, status, asset_id, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.ProjectID, u.NS, u.PresetID, u.PresetVersion, u.Visibility, u.Method, nullStr(u.TmpKey), nullStr(u.MultipartID), partSize, nullStr(u.Filename),
		nullStr(u.ContentType), u.DeclaredBytes, u.DeclaredSHA256, nullStr(u.SourceRef), meta, u.Status, nullStr(u.AssetID),
		ms(u.ExpiresAt), ms(u.CreatedAt), ms(u.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create upload: %w", mapErr(err))
	}
	return nil
}

// GetUpload returns an upload by id. Reads go through the writer inside a
// transaction (see InTx), otherwise through the reader pool.
func (s *Store) GetUpload(ctx context.Context, id string) (*Upload, error) {
	return scanUpload(s.r.QueryRowContext(ctx, `SELECT `+uploadCols+` FROM uploads WHERE id = ?`, id))
}

// TransitionUpload moves an upload from one of the from statuses to to,
// optionally setting asset_id. It reports false when the upload was not in
// any of the from statuses (a concurrent transition won).
func (s *Store) TransitionUpload(ctx context.Context, id string, from []string, to string, assetID *string, now time.Time) (bool, error) {
	q := `UPDATE uploads SET status = ?, updated_at = ?, asset_id = coalesce(?, asset_id) WHERE id = ? AND status IN (` +
		placeholders(len(from)) + `)`
	args := []any{to, ms(now), nullStr(assetID), id}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.w.ExecContext(ctx, q, args...)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListExpiredPendingUploads returns pending uploads whose ticket expired
// before cutoff.
func (s *Store) ListExpiredPendingUploads(ctx context.Context, cutoff time.Time, limit int) ([]*Upload, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+uploadCols+` FROM uploads WHERE status = 'pending' AND expires_at < ? ORDER BY expires_at LIMIT ?`,
		ms(cutoff), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Upload{}
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, mapErr(rows.Err())
}

// ListActiveServerUploadRefs returns source_ref of server-side uploads that
// are still waiting for or under processing (their staged files must stay).
func (s *Store) ListActiveServerUploadRefs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT source_ref FROM uploads WHERE method = 'SERVER' AND status IN ('pending','uploaded','processing') AND source_ref IS NOT NULL`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, mapErr(err)
		}
		out[ref] = true
	}
	return out, mapErr(rows.Err())
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
