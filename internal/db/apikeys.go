package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// APIKey is a project API key. Only the peppered hash of the secret is stored.
type APIKey struct {
	ID         string
	ProjectID  string
	Name       string
	Prefix     string
	SecretHash []byte
	Scopes     []string
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

const apiKeyCols = `id, project_id, name, prefix, secret_hash, scopes, last_used_at, expires_at, revoked_at, created_at`

func scanAPIKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var scopes string
	var last, exp, rev sql.NullInt64
	var created int64
	if err := row.Scan(&k.ID, &k.ProjectID, &k.Name, &k.Prefix, &k.SecretHash, &scopes, &last, &exp, &rev, &created); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(scopes), &k.Scopes); err != nil {
		return nil, fmt.Errorf("api key %s: scopes: %w", k.ID, err)
	}
	k.LastUsedAt, k.ExpiresAt, k.RevokedAt = fromNullMs(last), fromNullMs(exp), fromNullMs(rev)
	k.CreatedAt = fromMs(created)
	return &k, nil
}

// CreateAPIKey inserts a key.
func (s *Store) CreateAPIKey(ctx context.Context, k *APIKey) error {
	scopes, err := json.Marshal(k.Scopes)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx,
		`INSERT INTO api_keys (id, project_id, name, prefix, secret_hash, scopes, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.ProjectID, k.Name, k.Prefix, k.SecretHash, string(scopes), nullMs(k.ExpiresAt), ms(k.CreatedAt))
	if err != nil {
		return fmt.Errorf("create api key: %w", mapErr(err))
	}
	return nil
}

// ListAPIKeys returns all keys of a project, newest first (revoked included).
func (s *Store) ListAPIKeys(ctx context.Context, projectID string) ([]*APIKey, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE project_id = ? ORDER BY created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, mapErr(rows.Err())
}

// GetAPIKey returns a key by id.
func (s *Store) GetAPIKey(ctx context.Context, id string) (*APIKey, error) {
	return scanAPIKey(s.r.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id))
}

// GetAPIKeyByPrefix returns a key by its public prefix (kd_live_XXXXXXXX).
func (s *Store) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	return scanAPIKey(s.r.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE prefix = ?`, prefix))
}

// RevokeAPIKey marks a key revoked. Revoking an already revoked key is a no-op.
func (s *Store) RevokeAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, ms(at), id)
	return mapErr(err)
}

// TouchAPIKey updates last_used_at at most once per minInterval to avoid a
// write on every request.
func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time, minInterval time.Duration) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		ms(at), id, ms(at.Add(-minInterval)))
	return mapErr(err)
}
