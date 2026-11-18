package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Audit actor types.
const (
	ActorAdmin  = "admin"
	ActorAPIKey = "api_key"
	ActorSystem = "system"
	ActorAgent  = "agent"
)

// AuditEntry is one row of audit_log.
type AuditEntry struct {
	ID        int64
	ActorType string
	ActorID   *string
	ProjectID *string
	Action    string
	Target    *string
	Details   json.RawMessage // nil when absent
	IP        *string
	CreatedAt time.Time
}

// InsertAudit appends an audit record. details may be nil or any JSON-able value.
func (s *Store) InsertAudit(ctx context.Context, e *AuditEntry, details any) error {
	var dj sql.NullString
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("audit details: %w", err)
		}
		dj = sql.NullString{String: string(b), Valid: true}
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	res, err := s.w.ExecContext(ctx,
		`INSERT INTO audit_log (actor_type, actor_id, project_id, action, target, details_json, ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ActorType, nullStr(e.ActorID), nullStr(e.ProjectID), e.Action, nullStr(e.Target), dj, nullStr(e.IP), ms(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert audit: %w", mapErr(err))
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

// AuditFilter narrows ListAuditFiltered. Empty fields match everything;
// ProjectIDs (when non-nil) restricts entries to those projects.
type AuditFilter struct {
	ActorType  string
	ActorID    string
	ProjectID  string
	ProjectIDs []string
	BeforeID   int64
	Limit      int
}

// ListAudit returns up to limit entries with id < beforeID (0 = from newest),
// newest first.
func (s *Store) ListAudit(ctx context.Context, limit int, beforeID int64) ([]*AuditEntry, error) {
	return s.ListAuditFiltered(ctx, AuditFilter{Limit: limit, BeforeID: beforeID})
}

// ListAuditFiltered returns entries matching f, newest first.
func (s *Store) ListAuditFiltered(ctx context.Context, f AuditFilter) ([]*AuditEntry, error) {
	q := `SELECT id, actor_type, actor_id, project_id, action, target, details_json, ip, created_at FROM audit_log`
	var where []string
	args := []any{}
	if f.BeforeID > 0 {
		where = append(where, `id < ?`)
		args = append(args, f.BeforeID)
	}
	if f.ActorType != "" {
		where = append(where, `actor_type = ?`)
		args = append(args, f.ActorType)
	}
	if f.ActorID != "" {
		where = append(where, `actor_id = ?`)
		args = append(args, f.ActorID)
	}
	if f.ProjectID != "" {
		where = append(where, `project_id = ?`)
		args = append(args, f.ProjectID)
	}
	if f.ProjectIDs != nil {
		if len(f.ProjectIDs) == 0 {
			return []*AuditEntry{}, nil
		}
		where = append(where, `project_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(f.ProjectIDs)), ",")+`)`)
		for _, id := range f.ProjectIDs {
			args = append(args, id)
		}
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var actor, project, target, details, ip sql.NullString
		var created int64
		if err := rows.Scan(&e.ID, &e.ActorType, &actor, &project, &e.Action, &target, &details, &ip, &created); err != nil {
			return nil, mapErr(err)
		}
		e.ActorID, e.ProjectID, e.Target, e.IP = fromNullStr(actor), fromNullStr(project), fromNullStr(target), fromNullStr(ip)
		if details.Valid {
			e.Details = json.RawMessage(details.String)
		}
		e.CreatedAt = fromMs(created)
		out = append(out, &e)
	}
	return out, mapErr(rows.Err())
}
