package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Project is a tenant (e.g. acme, acme-staging).
type Project struct {
	ID               string
	Slug             string
	Name             string
	Environment      string // live | test
	StorageProfileID *string
	AllowedOrigins   []string
	QuotaBytes       *int64
	UsedBytes        int64
	ReservedBytes    int64
	DefaultMode      string // compact | balanced | quality
	DisabledAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const projectCols = `id, slug, name, environment, storage_profile_id, allowed_origins, quota_bytes,
	used_bytes, reserved_bytes, default_mode, disabled_at, created_at, updated_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var spID sql.NullString
	var origins string
	var quota, disabled sql.NullInt64
	var created, updated int64
	if err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Environment, &spID, &origins, &quota,
		&p.UsedBytes, &p.ReservedBytes, &p.DefaultMode, &disabled, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(origins), &p.AllowedOrigins); err != nil {
		return nil, fmt.Errorf("project %s: allowed_origins: %w", p.ID, err)
	}
	if p.AllowedOrigins == nil {
		p.AllowedOrigins = []string{}
	}
	p.StorageProfileID = fromNullStr(spID)
	p.QuotaBytes = fromNullInt(quota)
	p.DisabledAt = fromNullMs(disabled)
	p.CreatedAt, p.UpdatedAt = fromMs(created), fromMs(updated)
	return &p, nil
}

func originsJSON(o []string) string {
	if o == nil {
		o = []string{}
	}
	b, _ := json.Marshal(o)
	return string(b)
}

// CreateProject inserts a project. Returns ErrConflict if the slug is taken.
func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO projects (id, slug, name, environment, storage_profile_id, allowed_origins, quota_bytes,
		  default_mode, disabled_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Slug, p.Name, p.Environment, nullStr(p.StorageProfileID), originsJSON(p.AllowedOrigins),
		nullInt(p.QuotaBytes), defaultModeOr(p.DefaultMode), nullMs(p.DisabledAt), ms(p.CreatedAt), ms(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create project: %w", mapErr(err))
	}
	return nil
}

// UpdateProject writes all mutable fields of p.
func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE projects SET name = ?, storage_profile_id = ?, allowed_origins = ?, quota_bytes = ?,
		  default_mode = ?, disabled_at = ?, updated_at = ? WHERE id = ?`,
		p.Name, nullStr(p.StorageProfileID), originsJSON(p.AllowedOrigins), nullInt(p.QuotaBytes), defaultModeOr(p.DefaultMode),
		nullMs(p.DisabledAt), ms(p.UpdatedAt), p.ID)
	if err != nil {
		return fmt.Errorf("update project: %w", mapErr(err))
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetProject returns a project by id.
func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	return scanProject(s.r.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
}

// ListProjects returns all projects ordered by creation time.
func (s *Store) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+projectCols+` FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}

// CountStorageProfileUsage returns how many projects and blobs reference a
// storage profile.
func (s *Store) CountStorageProfileUsage(ctx context.Context, profileID string) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM projects WHERE storage_profile_id = ?)
		      + (SELECT count(*) FROM blobs WHERE storage_profile_id = ?)`, profileID, profileID).Scan(&n)
	return n, mapErr(err)
}

func defaultModeOr(m string) string {
	if m == "" {
		return "balanced"
	}
	return m
}
