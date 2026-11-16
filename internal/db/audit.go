package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Audit actor types.
const (
	ActorAdmin  = "admin"
	ActorAPIKey = "api_key"
	ActorSystem = "system"
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

// ListAudit returns up to limit entries with id < beforeID (0 = from newest),
// newest first.
func (s *Store) ListAudit(ctx context.Context, limit int, beforeID int64) ([]*AuditEntry, error) {
	q := `SELECT id, actor_type, actor_id, project_id, action, target, details_json, ip, created_at FROM audit_log`
	args := []any{}
	if beforeID > 0 {
		q += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
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
