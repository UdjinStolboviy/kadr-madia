package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Handler processes jobs of one kind.
type Handler struct {
	// Run does the work. A nil error marks the job succeeded; any other
	// error schedules a retry (or dead, see Permanent). ctx is cancelled when
	// the job is cancelled, its lease is lost, or on forced shutdown.
	Run func(ctx context.Context, j *Job) error
	// Dead is called once when the job reaches the dead state (attempts
	// exhausted, permanent error or lease expired on the last attempt). It
	// lets the owner settle domain state, e.g. mark the asset failed.
	Dead func(ctx context.Context, j *Job, cause error)
	// Lease is the lease duration (default DefaultLease). The runner extends
	// it every Lease/3 while Run is executing.
	Lease time.Duration
}

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	// Pools maps a pool name to its concurrency.
	Pools   map[string]int
	Logger  *slog.Logger
	Metrics *metrics.Set
	// PollInterval is how often idle workers re-check the queue (scheduled
	// retries become due without a wake-up). Default 1 s.
	PollInterval time.Duration
	// RecoverInterval is how often expired leases are recovered. Default 15 s.
	RecoverInterval time.Duration
}

// Runner runs worker goroutines per pool.
type Runner struct {
	q        *Queue
	cfg      RunnerConfig
	log      *slog.Logger
	handlers map[string]Handler
	id       string

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup // workers + recovery loop
	runCtx   context.Context
	cancel   context.CancelFunc

	mu     sync.Mutex
	active map[string]*activeJob
}

type activeJob struct {
	job    *Job
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRunner builds a runner. Register handlers before Start.
func NewRunner(q *Queue, cfg RunnerConfig) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.RecoverInterval <= 0 {
		cfg.RecoverInterval = 15 * time.Second
	}
	host, _ := os.Hostname()
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return &Runner{
		q: q, cfg: cfg, log: cfg.Logger, handlers: map[string]Handler{},
		id:     fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b)),
		stop:   make(chan struct{}),
		active: map[string]*activeJob{},
	}
}

// Handle registers the handler of a job kind.
func (r *Runner) Handle(kind string, h Handler) {
	if h.Lease <= 0 {
		h.Lease = DefaultLease
	}
	r.handlers[kind] = h
}

// Start launches the workers and the lease recovery loop. ctx is the parent
// of every job context.
func (r *Runner) Start(ctx context.Context) {
	r.runCtx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
	for pool, n := range r.cfg.Pools {
		if r.cfg.Metrics != nil {
			pool := pool
			r.cfg.Metrics.GetOrCreateGauge(fmt.Sprintf(`kadr_jobs_queued{pool=%q}`, pool), func() float64 {
				c, err := r.q.store.CountJobs(context.Background(), pool, db.JobQueued)
				if err != nil {
					return 0
				}
				return float64(c)
			})
		}
		for i := range max(1, n) {
			r.wg.Add(1)
			go r.worker(pool, fmt.Sprintf("%s/%s/%d", r.id, pool, i))
		}
	}
	r.wg.Add(1)
	go r.recoverLoop()
	r.log.Info("job runner started", "pools", r.cfg.Pools)
}

// Shutdown stops claiming new jobs and waits up to timeout for running jobs.
// Jobs still running afterwards are released back to the queue (the attempt
// is not counted) and their contexts are cancelled.
func (r *Runner) Shutdown(timeout time.Duration) {
	r.stopOnce.Do(func() { close(r.stop) })
	if r.cancel == nil {
		return // never started
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		r.cancel()
		r.log.Info("job runner stopped")
		return
	case <-time.After(timeout):
	}

	r.mu.Lock()
	unfinished := make([]*activeJob, 0, len(r.active))
	for _, a := range r.active {
		unfinished = append(unfinished, a)
	}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, a := range unfinished {
		if ok, err := r.q.Release(ctx, a.job); err != nil {
			r.log.Error("release job on shutdown", "job_id", a.job.ID, "err", err)
		} else if ok {
			r.log.Warn("job released on shutdown", "job_id", a.job.ID, "kind", a.job.Kind)
		}
		a.cancel()
	}
	// Give handlers a moment to observe cancellation and clean up.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		r.log.Warn("job handlers did not exit after cancellation")
	}
	r.cancel()
	r.log.Info("job runner stopped")
}

func (r *Runner) stopping() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *Runner) worker(pool, workerID string) {
	defer r.wg.Done()
	wake := r.q.wakeCh(pool)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		if r.stopping() {
			return
		}
		lease := DefaultLease
		j, err := r.q.Claim(r.runCtx, pool, workerID, lease)
		if err != nil {
			r.log.Error("claim job", "pool", pool, "err", err)
		}
		if j != nil {
			r.run(j)
			continue
		}
		timer.Reset(r.cfg.PollInterval)
		select {
		case <-r.stop:
			return
		case <-wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (r *Runner) run(j *Job) {
	h, ok := r.handlers[j.Kind]
	log := r.log.With("job_id", j.ID, "kind", j.Kind, "attempt", j.Attempts)
	if !ok {
		err := Permanent(fmt.Errorf("no handler for job kind %q", j.Kind))
		log.Error("job failed", "err", err)
		_, _ = r.q.Fail(r.runCtx, j, err)
		r.countFailure(j.Kind, err)
		return
	}
	// The claim used DefaultLease; apply the handler's lease right away.
	if h.Lease != DefaultLease {
		_, _ = r.q.Heartbeat(r.runCtx, j, h.Lease)
	}

	ctx, cancel := context.WithCancel(r.runCtx)
	a := &activeJob{job: j, cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	r.active[j.ID] = a
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.active, j.ID)
		r.mu.Unlock()
	}()

	go r.heartbeat(ctx, a, h.Lease, log)

	start := time.Now()
	err := safeRun(ctx, h.Run, j)
	close(a.done)
	dur := time.Since(start)
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.GetOrCreateHistogram(fmt.Sprintf(`kadr_job_duration_seconds{kind=%q}`, j.Kind)).Update(dur.Seconds())
	}

	// Persist the outcome even if ctx was cancelled by shutdown: the guarded
	// transitions are no-ops when the job was released or cancelled.
	octx, ocancel := context.WithTimeout(context.WithoutCancel(r.runCtx), 10*time.Second)
	defer ocancel()
	if err == nil {
		if _, serr := r.q.Succeed(octx, j); serr != nil {
			log.Error("mark job succeeded", "err", serr)
		}
		log.Debug("job succeeded", "duration_ms", dur.Milliseconds())
		return
	}
	r.countFailure(j.Kind, err)
	dead, ferr := r.q.Fail(octx, j, err)
	if ferr != nil {
		log.Error("record job failure", "err", ferr, "cause", err)
		return
	}
	if dead {
		log.Error("job dead", "err", err, "duration_ms", dur.Milliseconds())
		if h.Dead != nil {
			safeDead(octx, h.Dead, j, err, log)
		}
		return
	}
	log.Warn("job failed, will retry", "err", err, "duration_ms", dur.Milliseconds())
}

func (r *Runner) heartbeat(ctx context.Context, a *activeJob, lease time.Duration, log *slog.Logger) {
	t := time.NewTicker(max(lease/3, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-a.done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			ok, err := r.q.Heartbeat(context.WithoutCancel(ctx), a.job, lease)
			if err != nil {
				log.Warn("job heartbeat failed", "err", err)
				continue
			}
			if !ok {
				log.Warn("job lease lost (cancelled or recovered); stopping handler")
				a.cancel()
				return
			}
		}
	}
}

func (r *Runner) recoverLoop() {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.RecoverInterval)
	defer t.Stop()
	for {
		r.RecoverOnce(r.runCtx)
		select {
		case <-r.stop:
			return
		case <-t.C:
		}
	}
}

// RecoverOnce recovers expired leases and runs Dead handlers of jobs that
// ran out of attempts.
func (r *Runner) RecoverOnce(ctx context.Context) {
	jobs, err := r.q.RecoverExpired(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("recover expired jobs", "err", err)
		}
		return
	}
	for _, j := range jobs {
		r.log.Warn("job lease expired", "job_id", j.ID, "kind", j.Kind, "new_status", j.Status)
		if j.Status == db.JobDead {
			cause := errors.New("lease expired on the last attempt")
			r.countFailure(j.Kind, cause)
			if h, ok := r.handlers[j.Kind]; ok && h.Dead != nil {
				safeDead(ctx, h.Dead, j, cause, r.log)
			}
		}
	}
}

func (r *Runner) countFailure(kind string, err error) {
	if r.cfg.Metrics == nil {
		return
	}
	code := "error"
	var ce CodedError
	if errors.As(err, &ce) && ce.ErrorCode() != "" {
		code = ce.ErrorCode()
	} else if errors.Is(err, context.Canceled) {
		code = "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	r.cfg.Metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_job_failures_total{kind=%q,code=%q}`, kind, code)).Inc()
}

func safeRun(ctx context.Context, fn func(context.Context, *Job) error, j *Job) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic in job handler: %v\n%s", rec, debug.Stack())
		}
	}()
	return fn(ctx, j)
}

func safeDead(ctx context.Context, fn func(context.Context, *Job, error), j *Job, cause error, log *slog.Logger) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic in dead-letter handler", "job_id", j.ID, "panic", fmt.Sprint(rec))
		}
	}()
	fn(ctx, j, cause)
}
