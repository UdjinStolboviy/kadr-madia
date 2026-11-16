package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Preset is a named processing configuration with its current version params.
type Preset struct {
	ID             string
	ProjectID      string
	Slug           string
	Kind           string // image | video
	Description    string
	CurrentVersion int
	Params         json.RawMessage // params_json of CurrentVersion
	ArchivedAt     *time.Time
	CreatedAt      time.Time
}

// CreatePreset inserts a preset and its first immutable version.
func (s *Store) CreatePreset(ctx context.Context, p *Preset, createdBy string) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.w.ExecContext(ctx,
			`INSERT INTO presets (id, project_id, slug, kind, description, current_version, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.ProjectID, p.Slug, p.Kind, p.Description, p.CurrentVersion, ms(p.CreatedAt)); err != nil {
			return fmt.Errorf("create preset %s: %w", p.Slug, mapErr(err))
		}
		if _, err := tx.w.ExecContext(ctx,
			`INSERT INTO preset_versions (preset_id, version, params_json, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			p.ID, p.CurrentVersion, string(p.Params), createdBy, ms(p.CreatedAt)); err != nil {
			return fmt.Errorf("create preset version %s: %w", p.Slug, mapErr(err))
		}
		return nil
	})
}

// ListPresets returns the non-archived presets of a project with the params
// of their current version.
func (s *Store) ListPresets(ctx context.Context, projectID string) ([]*Preset, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT p.id, p.project_id, p.slug, p.kind, p.description, p.current_version, v.params_json, p.archived_at, p.created_at
		 FROM presets p JOIN preset_versions v ON v.preset_id = p.id AND v.version = p.current_version
		 WHERE p.project_id = ? AND p.archived_at IS NULL ORDER BY p.created_at, p.rowid`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Preset{}
	for rows.Next() {
		var p Preset
		var desc sql.NullString
		var params string
		var archived sql.NullInt64
		var created int64
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Slug, &p.Kind, &desc, &p.CurrentVersion, &params, &archived, &created); err != nil {
			return nil, mapErr(err)
		}
		p.Description = desc.String
		p.Params = json.RawMessage(params)
		p.ArchivedAt = fromNullMs(archived)
		p.CreatedAt = fromMs(created)
		out = append(out, &p)
	}
	return out, mapErr(rows.Err())
}
