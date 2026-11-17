package db

import (
	"context"
	"fmt"
	"time"
)

// PrefixCloudConnection is the id prefix of cloud connections.
const PrefixCloudConnection = "con"

// CloudConnection is an OAuth connection to a cloud drive used by imports.
type CloudConnection struct {
	ID           string
	ProjectID    string
	NS           *string
	Provider     string // gdrive | onedrive | dropbox
	AccountLabel *string
	TokenEnc     []byte
	RevokedAt    *time.Time
	CreatedAt    time.Time
}

const cloudCols = `id, project_id, ns, provider, account_label, token_enc, revoked_at, created_at`

func scanCloud(row interface{ Scan(...any) error }) (*CloudConnection, error) {
	var c CloudConnection
	var ns, label *string
	var revoked *int64
	var created int64
	if err := row.Scan(&c.ID, &c.ProjectID, &ns, &c.Provider, &label, &c.TokenEnc, &revoked, &created); err != nil {
		return nil, mapErr(err)
	}
	c.NS, c.AccountLabel = ns, label
	if revoked != nil {
		t := fromMs(*revoked)
		c.RevokedAt = &t
	}
	c.CreatedAt = fromMs(created)
	return &c, nil
}

// CreateCloudConnection inserts a connection.
func (s *Store) CreateCloudConnection(ctx context.Context, c *CloudConnection) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO cloud_connections (id, project_id, ns, provider, account_label, token_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.NS, c.Provider, c.AccountLabel, c.TokenEnc, ms(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("create cloud connection: %w", mapErr(err))
	}
	return nil
}

// GetCloudConnection returns a connection by id.
func (s *Store) GetCloudConnection(ctx context.Context, id string) (*CloudConnection, error) {
	return scanCloud(s.r.QueryRowContext(ctx, `SELECT `+cloudCols+` FROM cloud_connections WHERE id = ?`, id))
}

// ListCloudConnections returns the non-revoked connections of a project.
func (s *Store) ListCloudConnections(ctx context.Context, projectID string) ([]*CloudConnection, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+cloudCols+` FROM cloud_connections WHERE project_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*CloudConnection
	for rows.Next() {
		c, err := scanCloud(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// RevokeCloudConnection marks a connection revoked and wipes its token.
func (s *Store) RevokeCloudConnection(ctx context.Context, id string, now time.Time) (bool, error) {
	return s.execOne(ctx, `UPDATE cloud_connections SET revoked_at = ?, token_enc = x'' WHERE id = ? AND revoked_at IS NULL`, ms(now), id)
}
