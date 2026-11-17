package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Job statuses (jobs.status).
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobDead      = "dead"
	JobCancelled = "cancelled"
)

// PrefixJob is the id prefix of jobs.
const PrefixJob = "job"

// Job is one row of the lease-based queue.
type Job struct {
	ID          string
	Kind        string
	Pool        string
	Priority    int
	DedupeKey   *string
	Payload     json.RawMessage
	Status      string
	Attempts    int
	MaxAttempts int
	RunAt       time.Time
	LeaseUntil  *time.Time
	WorkerID    *string
	Progress    *int64
	LastError   *string
	CreatedAt   time.Time
	FinishedAt  *time.Time
}

const jobCols = `id, kind, pool, priority, dedupe_key, payload_json, status, attempts, max_attempts, run_at,
	lease_until, worker_id, progress, last_error, created_at, finished_at`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var dedupe, worker, lastErr sql.NullString
	var payload string
	var lease, progress, finished sql.NullInt64
	var runAt, created int64
	if err := row.Scan(&j.ID, &j.Kind, &j.Pool, &j.Priority, &dedupe, &payload, &j.Status, &j.Attempts, &j.MaxAttempts,
		&runAt, &lease, &worker, &progress, &lastErr, &created, &finished); err != nil {
		return nil, mapErr(err)
	}
	j.DedupeKey, j.WorkerID, j.LastError = fromNullStr(dedupe), fromNullStr(worker), fromNullStr(lastErr)
	j.Payload = json.RawMessage(payload)
	j.RunAt, j.CreatedAt = fromMs(runAt), fromMs(created)
	j.LeaseUntil, j.FinishedAt = fromNullMs(lease), fromNullMs(finished)
	j.Progress = fromNullInt(progress)
	return &j, nil
}

func scanJobs(rows *sql.Rows, err error) ([]*Job, error) {
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, mapErr(rows.Err())
}

// InsertJob enqueues j. When j.DedupeKey is set and a job with the same key
// already exists (in any status), nothing is inserted and the existing job id
// is returned with inserted=false.
func (s *Store) InsertJob(ctx context.Context, j *Job) (id string, inserted bool, err error) {
	payload := string(j.Payload)
	if payload == "" {
		payload = "{}"
	}
	res, err := s.w.ExecContext(ctx,
		`INSERT INTO jobs (id, kind, pool, priority, dedupe_key, payload_json, status, attempts, max_attempts, run_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'queued', 0, ?, ?, ?)
		 ON CONFLICT(dedupe_key) DO NOTHING`,
		j.ID, j.Kind, j.Pool, j.Priority, nullStr(j.DedupeKey), payload, j.MaxAttempts, ms(j.RunAt), ms(j.CreatedAt))
	if err != nil {
		return "", false, fmt.Errorf("insert job: %w", mapErr(err))
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return j.ID, true, nil
	}
	err = s.w.QueryRowContext(ctx, `SELECT id FROM jobs WHERE dedupe_key = ?`, nullStr(j.DedupeKey)).Scan(&id)
	if err != nil {
		return "", false, fmt.Errorf("lookup deduplicated job: %w", mapErr(err))
	}
	return id, false, nil
}

// ClaimJob atomically leases the next runnable job of pool (lowest priority
// value first, then oldest run_at). It increments attempts. Returns
// ErrNotFound when nothing is runnable.
func (s *Store) ClaimJob(ctx context.Context, pool, workerID string, now, leaseUntil time.Time) (*Job, error) {
	return scanJob(s.w.QueryRowContext(ctx,
		`UPDATE jobs SET status = 'running', attempts = attempts + 1, worker_id = ?, lease_until = ?
		 WHERE id = (SELECT id FROM jobs WHERE pool = ? AND status = 'queued' AND run_at <= ?
		             ORDER BY priority, run_at, id LIMIT 1)
		   AND status = 'queued'
		 RETURNING `+jobCols,
		workerID, ms(leaseUntil), pool, ms(now)))
}

// ExtendJobLease moves lease_until of a job still owned by workerID. It
// reports false when the job is no longer running under this worker
// (cancelled, released or recovered after lease expiry).
func (s *Store) ExtendJobLease(ctx context.Context, id, workerID string, leaseUntil time.Time) (bool, error) {
	return s.execOne(ctx, `UPDATE jobs SET lease_until = ? WHERE id = ? AND status = 'running' AND worker_id = ?`,
		ms(leaseUntil), id, workerID)
}

// SetJobProgress records progress (0–100) of a running job.
func (s *Store) SetJobProgress(ctx context.Context, id, workerID string, progress int) error {
	_, err := s.w.ExecContext(ctx, `UPDATE jobs SET progress = ? WHERE id = ? AND status = 'running' AND worker_id = ?`,
		progress, id, workerID)
	return mapErr(err)
}

// SucceedJob marks a job owned by workerID as succeeded.
func (s *Store) SucceedJob(ctx context.Context, id, workerID string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE jobs SET status = 'succeeded', finished_at = ?, lease_until = NULL, last_error = NULL
		 WHERE id = ? AND status = 'running' AND worker_id = ?`, ms(now), id, workerID)
}

// RetryJob puts a job owned by workerID back into the queue at runAt.
func (s *Store) RetryJob(ctx context.Context, id, workerID string, runAt time.Time, lastErr string) (bool, error) {
	return s.execOne(ctx,
		`UPDATE jobs SET status = 'queued', run_at = ?, lease_until = NULL, worker_id = NULL, last_error = ?
		 WHERE id = ? AND status = 'running' AND worker_id = ?`, ms(runAt), lastErr, id, workerID)
}

// KillJob moves a job owned by workerID to the dead-letter state.
func (s *Store) KillJob(ctx context.Context, id, workerID string, now time.Time, lastErr string) (bool, error) {
	return s.execOne(ctx,
		`UPDATE jobs SET status = 'dead', finished_at = ?, lease_until = NULL, last_error = ?
		 WHERE id = ? AND status = 'running' AND worker_id = ?`, ms(now), lastErr, id, workerID)
}

// ReleaseJob returns a job owned by workerID to the queue without counting
// the attempt (graceful shutdown).
func (s *Store) ReleaseJob(ctx context.Context, id, workerID string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE jobs SET status = 'queued', run_at = ?, lease_until = NULL, worker_id = NULL,
		   attempts = max(0, attempts - 1), last_error = 'released on shutdown'
		 WHERE id = ? AND status = 'running' AND worker_id = ?`, ms(now), id, workerID)
}

// CancelJob cancels a queued or running job. A running job's worker notices
// on its next heartbeat. Returns false if the job was already finished.
func (s *Store) CancelJob(ctx context.Context, id string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE jobs SET status = 'cancelled', finished_at = ?, lease_until = NULL
		 WHERE id = ? AND status IN ('queued', 'running')`, ms(now), id)
}

// RequeueJob moves a dead or cancelled job back to the queue with a fresh
// attempt budget (admin "Retry").
func (s *Store) RequeueJob(ctx context.Context, id string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE jobs SET status = 'queued', attempts = 0, run_at = ?, finished_at = NULL, worker_id = NULL, lease_until = NULL
		 WHERE id = ? AND status IN ('dead', 'cancelled')`, ms(now), id)
}

// RecoverExpiredJobs returns running jobs whose lease expired (crashed or
// stuck workers) to the queue, or to dead when their attempts are exhausted.
// It returns the affected jobs with their new status.
func (s *Store) RecoverExpiredJobs(ctx context.Context, now time.Time) ([]*Job, error) {
	return scanJobs(s.w.QueryContext(ctx,
		`UPDATE jobs SET
		   status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'queued' END,
		   finished_at = CASE WHEN attempts >= max_attempts THEN ? ELSE NULL END,
		   run_at = ?, lease_until = NULL, worker_id = NULL, last_error = 'lease expired'
		 WHERE status = 'running' AND lease_until < ?
		 RETURNING `+jobCols, ms(now), ms(now), ms(now)))
}

// GetJob returns a job by id.
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	return scanJob(s.r.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
}

// GetJobByDedupeKey returns the job with the given dedupe key.
func (s *Store) GetJobByDedupeKey(ctx context.Context, key string) (*Job, error) {
	return scanJob(s.r.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE dedupe_key = ?`, key))
}

// CountJobs returns the number of jobs of a pool in status.
func (s *Store) CountJobs(ctx context.Context, pool, status string) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE pool = ? AND status = ?`, pool, status).Scan(&n)
	return n, mapErr(err)
}

func (s *Store) execOne(ctx context.Context, q string, args ...any) (bool, error) {
	res, err := s.w.ExecContext(ctx, q, args...)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
