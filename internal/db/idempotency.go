package db

import (
	"context"
	"fmt"
	"time"
)

// IdempotencyRecord is a stored response for an Idempotency-Key.
// ResponseCode 0 marks a request that is still in progress.
type IdempotencyRecord struct {
	ProjectID    string
	Key          string
	RequestHash  []byte
	ResponseCode int
	ResponseBody string
	CreatedAt    time.Time
}

// ReserveIdempotencyKey inserts an in-progress record. If the key already
// exists, the existing record is returned with created=false.
func (s *Store) ReserveIdempotencyKey(ctx context.Context, projectID, key string, hash []byte, now time.Time) (*IdempotencyRecord, bool, error) {
	res, err := s.w.ExecContext(ctx,
		`INSERT INTO idempotency_keys (project_id, key, request_hash, response_code, response_body, created_at)
		 VALUES (?, ?, ?, 0, '', ?) ON CONFLICT(project_id, key) DO NOTHING`, projectID, key, hash, ms(now))
	if err != nil {
		return nil, false, fmt.Errorf("reserve idempotency key: %w", mapErr(err))
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return &IdempotencyRecord{ProjectID: projectID, Key: key, RequestHash: hash, CreatedAt: now}, true, nil
	}
	var r IdempotencyRecord
	var created int64
	err = s.w.QueryRowContext(ctx,
		`SELECT project_id, key, request_hash, response_code, response_body, created_at FROM idempotency_keys
		 WHERE project_id = ? AND key = ?`, projectID, key).
		Scan(&r.ProjectID, &r.Key, &r.RequestHash, &r.ResponseCode, &r.ResponseBody, &created)
	if err != nil {
		return nil, false, mapErr(err)
	}
	r.CreatedAt = fromMs(created)
	return &r, false, nil
}

// CompleteIdempotencyKey stores the final response of a reserved key.
func (s *Store) CompleteIdempotencyKey(ctx context.Context, projectID, key string, code int, body string) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE idempotency_keys SET response_code = ?, response_body = ? WHERE project_id = ? AND key = ?`,
		code, body, projectID, key)
	return mapErr(err)
}

// DeleteIdempotencyKey drops a key (the request failed in a retryable way).
func (s *Store) DeleteIdempotencyKey(ctx context.Context, projectID, key string) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE project_id = ? AND key = ?`, projectID, key)
	return mapErr(err)
}

// ───────────── retention ─────────────

// RetentionCutoffs are the "older than" thresholds of DATA_MODEL.md.
type RetentionCutoffs struct {
	Events         time.Time // events (+ their deliveries by cascade)
	Deliveries     time.Time // finished deliveries
	Jobs           time.Time // succeeded / cancelled jobs
	DeadJobs       time.Time // dead jobs (kept longer for investigation); zero = keep
	IdempotencyKey time.Time
	AuditLog       time.Time
}

// RetentionResult counts deleted rows per table.
type RetentionResult struct {
	Events, Deliveries, Jobs, IdempotencyKeys, AuditLog int64
}

// PurgeExpired deletes rows past their retention period.
func (s *Store) PurgeExpired(ctx context.Context, c RetentionCutoffs) (RetentionResult, error) {
	var r RetentionResult
	exec := func(dst *int64, q string, args ...any) error {
		res, err := s.w.ExecContext(ctx, q, args...)
		if err != nil {
			return mapErr(err)
		}
		*dst, _ = res.RowsAffected()
		return nil
	}
	if err := exec(&r.Deliveries,
		`DELETE FROM webhook_deliveries WHERE status IN ('delivered', 'dead')
		   AND event_id IN (SELECT id FROM events WHERE created_at < ?)`, ms(c.Deliveries)); err != nil {
		return r, err
	}
	// Events are only removed once none of their deliveries is still in flight.
	if err := exec(&r.Events,
		`DELETE FROM events WHERE created_at < ?
		   AND NOT EXISTS (SELECT 1 FROM webhook_deliveries d WHERE d.event_id = events.id AND d.status IN ('pending', 'delivering'))`,
		ms(c.Events)); err != nil {
		return r, err
	}
	if err := exec(&r.Jobs, `DELETE FROM jobs WHERE status IN ('succeeded', 'cancelled') AND finished_at < ?`, ms(c.Jobs)); err != nil {
		return r, err
	}
	if !c.DeadJobs.IsZero() {
		var dead int64
		if err := exec(&dead, `DELETE FROM jobs WHERE status = 'dead' AND finished_at < ?`, ms(c.DeadJobs)); err != nil {
			return r, err
		}
		r.Jobs += dead
	}
	if err := exec(&r.IdempotencyKeys, `DELETE FROM idempotency_keys WHERE created_at < ?`, ms(c.IdempotencyKey)); err != nil {
		return r, err
	}
	if err := exec(&r.AuditLog, `DELETE FROM audit_log WHERE created_at < ?`, ms(c.AuditLog)); err != nil {
		return r, err
	}
	return r, nil
}
