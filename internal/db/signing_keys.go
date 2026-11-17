package db

import (
	"context"
	"fmt"
	"time"
)

// Signing key statuses (signing_keys.status).
const (
	SigningActive     = "active"      // used for new signatures and accepted by the edge
	SigningVerifyOnly = "verify_only" // accepted by the edge until URLs signed with it expire
	SigningRetired    = "retired"
)

// SigningKey is an HMAC key for private URLs verified by kadr-edge.
type SigningKey struct {
	ID          string // kid
	ProjectID   string
	SecretEnc   []byte
	Status      string
	PublishedAt *time.Time
	CreatedAt   time.Time
}

const signingCols = `id, project_id, secret_enc, status, published_at, created_at`

func scanSigningKey(row interface{ Scan(...any) error }) (*SigningKey, error) {
	var k SigningKey
	var pub *int64
	var created int64
	if err := row.Scan(&k.ID, &k.ProjectID, &k.SecretEnc, &k.Status, &pub, &created); err != nil {
		return nil, mapErr(err)
	}
	if pub != nil {
		t := fromMs(*pub)
		k.PublishedAt = &t
	}
	k.CreatedAt = fromMs(created)
	return &k, nil
}

// RotateSigningKey inserts k as the new active key of its project and demotes
// the previous active key to verify_only, atomically.
func (s *Store) RotateSigningKey(ctx context.Context, k *SigningKey) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.w.ExecContext(ctx,
			`UPDATE signing_keys SET status = 'verify_only' WHERE project_id = ? AND status = 'active'`, k.ProjectID); err != nil {
			return mapErr(err)
		}
		_, err := tx.w.ExecContext(ctx,
			`INSERT INTO signing_keys (id, project_id, secret_enc, status, created_at) VALUES (?, ?, ?, 'active', ?)`,
			k.ID, k.ProjectID, k.SecretEnc, ms(k.CreatedAt))
		if err != nil {
			return fmt.Errorf("create signing key: %w", mapErr(err))
		}
		return nil
	})
}

// GetActiveSigningKey returns the active key of a project (ErrNotFound if none).
func (s *Store) GetActiveSigningKey(ctx context.Context, projectID string) (*SigningKey, error) {
	return scanSigningKey(s.r.QueryRowContext(ctx,
		`SELECT `+signingCols+` FROM signing_keys WHERE project_id = ? AND status = 'active'`, projectID))
}

// GetSigningKey returns a key by kid.
func (s *Store) GetSigningKey(ctx context.Context, id string) (*SigningKey, error) {
	return scanSigningKey(s.r.QueryRowContext(ctx, `SELECT `+signingCols+` FROM signing_keys WHERE id = ?`, id))
}

// ListSigningKeys returns the non-retired keys of a project (all projects when
// projectID is empty), newest first.
func (s *Store) ListSigningKeys(ctx context.Context, projectID string, includeRetired bool) ([]*SigningKey, error) {
	q := `SELECT ` + signingCols + ` FROM signing_keys WHERE 1=1`
	var args []any
	if projectID != "" {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if !includeRetired {
		q += ` AND status != 'retired'`
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*SigningKey
	for rows.Next() {
		k, err := scanSigningKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, mapErr(rows.Err())
}

// RetireSigningKey marks a key retired; the edge stops accepting it.
func (s *Store) RetireSigningKey(ctx context.Context, id string) (bool, error) {
	return s.execOne(ctx, `UPDATE signing_keys SET status = 'retired' WHERE id = ? AND status != 'retired'`, id)
}

// MarkSigningKeysPublished records that all non-retired keys were synced to the edge.
func (s *Store) MarkSigningKeysPublished(ctx context.Context, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE signing_keys SET published_at = ? WHERE status != 'retired'`, ms(now))
	return mapErr(err)
}
