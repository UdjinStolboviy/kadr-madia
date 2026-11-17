// Package queue implements the lease-based job queue on the SQLite jobs
// table (docs/ARCHITECTURE.md §6, ADR-0003).
//
// State machine:
//
//	queued → running (claim: attempts++, worker_id, lease_until)
//	running → succeeded
//	running → queued   (error with attempts left: exponential backoff + jitter)
//	running → queued   (lease expired: crashed or stuck worker)
//	running → queued   (graceful shutdown: released, attempt not counted)
//	running → dead     (attempts exhausted or permanent error)
//	queued|running → cancelled
//	dead|cancelled → queued (manual retry)
//
// Every transition of a running job is guarded by worker_id, so a worker that
// lost its lease can never overwrite the state written by someone else.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Pool names.
const (
	PoolImage       = "image"
	PoolMaintenance = "maintenance"
	PoolWebhook     = "webhook"
)

// Defaults.
const (
	DefaultPriority    = 100
	DefaultMaxAttempts = 5
	DefaultLease       = 2 * time.Minute

	// DefaultBackoffBase and DefaultBackoffCap bound the retry delay.
	DefaultBackoffBase = 5 * time.Second
	DefaultBackoffCap  = 15 * time.Minute
)

// ErrPermanent marks an error that must not be retried.
var ErrPermanent = errors.New("permanent job failure")

// Permanent wraps err so the queue moves the job straight to dead.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrPermanent, err)
}

// CodedError is implemented by errors that carry a short machine code used
// as the "code" label of kadr_job_failures_total.
type CodedError interface {
	ErrorCode() string
}

// Queue wraps the jobs table.
type Queue struct {
	store *db.Store
	now   func() time.Time
	rand  func() float64

	backoffBase, backoffCap time.Duration

	mu   sync.Mutex
	wake map[string]chan struct{}
}

// New returns a queue over store. now may be nil (time.Now).
func New(store *db.Store, now func() time.Time) *Queue {
	if now == nil {
		now = time.Now
	}
	return &Queue{store: store, now: now, rand: rand.Float64, wake: map[string]chan struct{}{},
		backoffBase: DefaultBackoffBase, backoffCap: DefaultBackoffCap}
}

// SetBackoff overrides the retry delay bounds (tests, tuning).
func (q *Queue) SetBackoff(base, cap time.Duration) {
	q.backoffBase, q.backoffCap = base, cap
}

// Store returns the underlying store.
func (q *Queue) Store() *db.Store { return q.store }

// Job is a queued job as seen by handlers.
type Job = db.Job

// EnqueueOptions describes a new job.
type EnqueueOptions struct {
	Kind        string
	Pool        string
	Payload     any    // marshalled to JSON
	DedupeKey   string // "" = none; otherwise unique across all jobs ever enqueued
	Priority    int    // lower runs earlier; 0 = DefaultPriority
	RunAt       time.Time
	MaxAttempts int // 0 = DefaultMaxAttempts
}

// Enqueue inserts a job using tx (a transaction-scoped store, or nil for the
// queue's own store). With a DedupeKey that already exists, the existing job
// id is returned with created=false. Workers of the pool are woken up; when
// tx is a transaction the claim simply waits for the single writer
// connection, so the job is visible as soon as the transaction commits.
func (q *Queue) Enqueue(ctx context.Context, tx *db.Store, o EnqueueOptions) (id string, created bool, err error) {
	if o.Kind == "" || o.Pool == "" {
		return "", false, errors.New("queue: kind and pool are required")
	}
	payload, err := json.Marshal(o.Payload)
	if err != nil {
		return "", false, fmt.Errorf("queue: marshal payload: %w", err)
	}
	if o.Payload == nil {
		payload = []byte("{}")
	}
	now := q.now()
	j := &db.Job{
		ID: db.NewID(db.PrefixJob), Kind: o.Kind, Pool: o.Pool, Priority: o.Priority, Payload: payload,
		MaxAttempts: o.MaxAttempts, RunAt: o.RunAt, CreatedAt: now,
	}
	if j.Priority == 0 {
		j.Priority = DefaultPriority
	}
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = DefaultMaxAttempts
	}
	if j.RunAt.IsZero() {
		j.RunAt = now
	}
	if o.DedupeKey != "" {
		k := o.DedupeKey
		j.DedupeKey = &k
	}
	if tx == nil {
		tx = q.store
	}
	id, created, err = tx.InsertJob(ctx, j)
	if err != nil {
		return "", false, err
	}
	if created {
		q.Notify(o.Pool)
	}
	return id, created, nil
}

// Notify wakes up one idle worker of pool.
func (q *Queue) Notify(pool string) {
	select {
	case q.wakeCh(pool) <- struct{}{}:
	default:
	}
}

func (q *Queue) wakeCh(pool string) chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch, ok := q.wake[pool]
	if !ok {
		ch = make(chan struct{}, 1)
		q.wake[pool] = ch
	}
	return ch
}

// Claim leases the next runnable job of pool for lease. Returns (nil, nil)
// when the pool is empty.
func (q *Queue) Claim(ctx context.Context, pool, workerID string, lease time.Duration) (*Job, error) {
	now := q.now()
	j, err := q.store.ClaimJob(ctx, pool, workerID, now, now.Add(lease))
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return j, err
}

// Heartbeat extends the lease of a running job. It returns false when the
// worker no longer owns the job (cancelled, released or recovered).
func (q *Queue) Heartbeat(ctx context.Context, j *Job, lease time.Duration) (bool, error) {
	return q.store.ExtendJobLease(ctx, j.ID, deref(j.WorkerID), q.now().Add(lease))
}

// Succeed marks a job succeeded.
func (q *Queue) Succeed(ctx context.Context, j *Job) (bool, error) {
	return q.store.SucceedJob(ctx, j.ID, deref(j.WorkerID), q.now())
}

// Fail records a failed attempt. The job is retried with backoff unless the
// error is permanent or attempts are exhausted, in which case it becomes
// dead and dead=true is returned (only if this call made the transition).
func (q *Queue) Fail(ctx context.Context, j *Job, cause error) (dead bool, err error) {
	msg := "unknown error"
	if cause != nil {
		msg = cause.Error()
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if errors.Is(cause, ErrPermanent) || j.Attempts >= j.MaxAttempts {
		return q.store.KillJob(ctx, j.ID, deref(j.WorkerID), q.now(), msg)
	}
	_, err = q.store.RetryJob(ctx, j.ID, deref(j.WorkerID), q.now().Add(q.Backoff(j.Attempts)), msg)
	return false, err
}

// Release returns a running job to the queue without counting the attempt.
func (q *Queue) Release(ctx context.Context, j *Job) (bool, error) {
	ok, err := q.store.ReleaseJob(ctx, j.ID, deref(j.WorkerID), q.now())
	if ok {
		q.Notify(j.Pool)
	}
	return ok, err
}

// Cancel cancels a queued or running job.
func (q *Queue) Cancel(ctx context.Context, id string) (bool, error) {
	return q.store.CancelJob(ctx, id, q.now())
}

// Retry requeues a dead or cancelled job with a fresh attempt budget.
func (q *Queue) Retry(ctx context.Context, id string) (bool, error) {
	ok, err := q.store.RequeueJob(ctx, id, q.now())
	if ok {
		if j, err := q.store.GetJob(ctx, id); err == nil {
			q.Notify(j.Pool)
		}
	}
	return ok, err
}

// RecoverExpired requeues (or kills) running jobs whose lease expired and
// returns them with their new status.
func (q *Queue) RecoverExpired(ctx context.Context) ([]*Job, error) {
	jobs, err := q.store.RecoverExpiredJobs(ctx, q.now())
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Status == db.JobQueued {
			q.Notify(j.Pool)
		}
	}
	return jobs, nil
}

// Backoff returns the delay before retry number attempt (1-based):
// base·2^(attempt−1) capped at the backoff cap, with "equal jitter" so the delay
// is uniformly distributed in [d/2, d].
func (q *Queue) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := q.backoffBase
	for i := 1; i < attempt && d < q.backoffCap; i++ {
		d *= 2
	}
	d = min(d, q.backoffCap)
	half := d / 2
	return half + time.Duration(q.rand()*float64(half))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
