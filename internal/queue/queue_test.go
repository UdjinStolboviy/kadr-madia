package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestEnqueueDedupe(t *testing.T) {
	ctx := context.Background()
	q := New(dbtest.Open(t), nil)
	id1, created, err := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", Payload: map[string]string{"a": "b"}, DedupeKey: "d1"})
	if err != nil || !created {
		t.Fatalf("first enqueue: %v %v", created, err)
	}
	id2, created, err := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", DedupeKey: "d1"})
	if err != nil || created || id2 != id1 {
		t.Fatalf("dedupe: id1=%s id2=%s created=%v err=%v", id1, id2, created, err)
	}
	j, err := q.Store().GetJob(ctx, id1)
	if err != nil || string(j.Payload) != `{"a":"b"}` || j.MaxAttempts != DefaultMaxAttempts || j.Priority != DefaultPriority {
		t.Fatalf("job: %+v %v", j, err)
	}
}

func TestClaimExclusivityUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	q := New(dbtest.Open(t), nil)
	const n = 60
	for i := range n {
		if _, _, err := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", Payload: i}); err != nil {
			t.Fatal(err)
		}
	}
	// A job in another pool must never be claimed by "p".
	q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "other"})

	var mu sync.Mutex
	seen := map[string]string{}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := fmt.Sprintf("w%d", w)
			for {
				j, err := q.Claim(ctx, "p", worker, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if j == nil {
					return
				}
				mu.Lock()
				if prev, dup := seen[j.ID]; dup {
					t.Errorf("job %s claimed by %s and %s", j.ID, prev, worker)
				}
				seen[j.ID] = worker
				mu.Unlock()
				if j.Attempts != 1 || j.Status != db.JobRunning || *j.WorkerID != worker {
					t.Errorf("claimed job state: %+v", j)
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d jobs, want %d", len(seen), n)
	}
}

func TestClaimOrderPriorityAndRunAt(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Now()}
	q := New(dbtest.Open(t), c.Now)
	late, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", RunAt: c.Now().Add(time.Hour)})
	low, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", Priority: 200})
	high, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", Priority: 10})
	for _, want := range []string{high, low} {
		j, _ := q.Claim(ctx, "p", "w", time.Minute)
		if j == nil || j.ID != want {
			t.Fatalf("claimed %+v, want %s", j, want)
		}
	}
	if j, _ := q.Claim(ctx, "p", "w", time.Minute); j != nil {
		t.Fatalf("future job claimed early: %s", j.ID)
	}
	c.Add(time.Hour)
	if j, _ := q.Claim(ctx, "p", "w", time.Minute); j == nil || j.ID != late {
		t.Fatalf("due job not claimed: %+v", j)
	}
}

func TestBackoffAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Now().Truncate(time.Millisecond)}
	q := New(dbtest.Open(t), c.Now)

	// Backoff grows exponentially with jitter in [d/2, d] and is capped.
	for attempt, full := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 12: DefaultBackoffCap} {
		for range 50 {
			d := q.Backoff(attempt)
			if d < full/2 || d > full {
				t.Fatalf("Backoff(%d) = %v, want within [%v, %v]", attempt, d, full/2, full)
			}
		}
	}
	q.rand = func() float64 { return 1 } // deterministic: full delay

	id, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", MaxAttempts: 3})
	for attempt := 1; attempt <= 3; attempt++ {
		j, err := q.Claim(ctx, "p", "w", time.Minute)
		if err != nil || j == nil || j.Attempts != attempt {
			t.Fatalf("claim %d: %+v %v", attempt, j, err)
		}
		dead, err := q.Fail(ctx, j, fmt.Errorf("boom %d", attempt))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := q.Store().GetJob(ctx, id)
		if attempt < 3 {
			wantRunAt := c.Now().Add(q.Backoff(attempt))
			if dead || got.Status != db.JobQueued || !got.RunAt.Equal(wantRunAt) || got.WorkerID != nil {
				t.Fatalf("after failure %d: dead=%v %+v (want run_at %v)", attempt, dead, got, wantRunAt)
			}
			c.Add(q.Backoff(attempt))
		} else if !dead || got.Status != db.JobDead || got.FinishedAt == nil || *got.LastError != "boom 3" {
			t.Fatalf("expected dead letter: dead=%v %+v", dead, got)
		}
	}

	// Permanent errors skip retries; manual retry revives a dead job.
	id2, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p2"})
	j, _ := q.Claim(ctx, "p2", "w", time.Minute)
	if dead, _ := q.Fail(ctx, j, Permanent(errors.New("bad input"))); !dead {
		t.Fatal("permanent error was retried")
	}
	if ok, _ := q.Retry(ctx, id2); !ok {
		t.Fatal("retry of dead job failed")
	}
	if j, _ := q.Claim(ctx, "p2", "w", time.Minute); j == nil || j.Attempts != 1 {
		t.Fatalf("retried job: %+v", j)
	}
}

func TestLeaseExpiryRecovery(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Now()}
	q := New(dbtest.Open(t), c.Now)
	id, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "k", Pool: "p", MaxAttempts: 2})

	j, _ := q.Claim(ctx, "p", "crashed-worker", time.Minute)
	if rec, _ := q.RecoverExpired(ctx); len(rec) != 0 {
		t.Fatalf("recovered a live lease: %+v", rec)
	}
	c.Add(2 * time.Minute)
	rec, err := q.RecoverExpired(ctx)
	if err != nil || len(rec) != 1 || rec[0].ID != id || rec[0].Status != db.JobQueued {
		t.Fatalf("recover: %+v %v", rec, err)
	}
	// The crashed worker can no longer change the job.
	if ok, _ := q.Succeed(ctx, j); ok {
		t.Fatal("stale worker succeeded a recovered job")
	}
	j2, _ := q.Claim(ctx, "p", "w2", time.Minute)
	if j2 == nil || j2.Attempts != 2 {
		t.Fatalf("reclaim: %+v", j2)
	}
	c.Add(2 * time.Minute)
	rec, _ = q.RecoverExpired(ctx)
	if len(rec) != 1 || rec[0].Status != db.JobDead {
		t.Fatalf("expired on last attempt should be dead: %+v", rec)
	}
}

func TestRunnerRunsRetriesAndDeadLetters(t *testing.T) {
	ctx := context.Background()
	q := New(dbtest.Open(t), nil)
	q.SetBackoff(time.Millisecond, 5*time.Millisecond)
	set := metrics.NewSet()
	r := NewRunner(q, RunnerConfig{Pools: map[string]int{"p": 3}, Logger: quietLog(), Metrics: set, PollInterval: 5 * time.Millisecond})

	var okRuns, flakyRuns atomic.Int32
	deadCh := make(chan string, 1)
	r.Handle("ok", Handler{Run: func(context.Context, *Job) error { okRuns.Add(1); return nil }})
	r.Handle("flaky", Handler{Run: func(context.Context, *Job) error {
		if flakyRuns.Add(1) < 2 {
			return errors.New("transient")
		}
		return nil
	}})
	r.Handle("broken", Handler{
		Run:  func(context.Context, *Job) error { return errors.New("always fails") },
		Dead: func(_ context.Context, j *Job, cause error) { deadCh <- j.ID + ": " + cause.Error() },
	})
	r.Start(ctx)
	defer r.Shutdown(time.Second)

	for range 5 {
		q.Enqueue(ctx, nil, EnqueueOptions{Kind: "ok", Pool: "p"})
	}
	flaky, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "flaky", Pool: "p"})
	broken, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "broken", Pool: "p", MaxAttempts: 3})

	select {
	case msg := <-deadCh:
		if msg != broken+": always fails" {
			t.Fatalf("dead callback: %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broken job never reached dead")
	}
	waitFor(t, func() bool {
		j, _ := q.Store().GetJob(ctx, flaky)
		return j.Status == db.JobSucceeded && okRuns.Load() == 5
	})
	j, _ := q.Store().GetJob(ctx, broken)
	if j.Status != db.JobDead || j.Attempts != 3 {
		t.Fatalf("broken job: %+v", j)
	}
	var buf metricsBuf
	set.WritePrometheus(&buf)
	for _, want := range []string{`kadr_job_failures_total{kind="broken",code="error"} 3`, `kadr_jobs_queued{pool="p"} 0`, `kadr_job_duration_seconds_bucket{kind="ok"`} {
		if !buf.contains(want) {
			t.Fatalf("metrics missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunnerCancelStopsHandler(t *testing.T) {
	ctx := context.Background()
	q := New(dbtest.Open(t), nil)
	r := NewRunner(q, RunnerConfig{Pools: map[string]int{"p": 1}, Logger: quietLog(), PollInterval: 5 * time.Millisecond})
	started := make(chan struct{})
	stopped := make(chan error, 1)
	r.Handle("long", Handler{Lease: 60 * time.Millisecond, Run: func(ctx context.Context, _ *Job) error {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return ctx.Err()
	}})
	r.Start(ctx)
	defer r.Shutdown(time.Second)
	id, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "long", Pool: "p"})
	<-started
	if ok, _ := q.Cancel(ctx, id); !ok {
		t.Fatal("cancel failed")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not cancelled via heartbeat")
	}
	waitFor(t, func() bool { j, _ := q.Store().GetJob(ctx, id); return j.Status == db.JobCancelled })
}

func TestRunnerGracefulShutdownReleasesLeases(t *testing.T) {
	ctx := context.Background()
	q := New(dbtest.Open(t), nil)
	r := NewRunner(q, RunnerConfig{Pools: map[string]int{"p": 2}, Logger: quietLog(), PollInterval: 5 * time.Millisecond})
	var quickDone atomic.Bool
	started := make(chan struct{}, 1)
	r.Handle("quick", Handler{Run: func(context.Context, *Job) error {
		time.Sleep(20 * time.Millisecond)
		quickDone.Store(true)
		return nil
	}})
	r.Handle("stuck", Handler{Run: func(ctx context.Context, _ *Job) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}})
	r.Start(ctx)
	stuck, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "stuck", Pool: "p"})
	<-started
	quick, _, _ := q.Enqueue(ctx, nil, EnqueueOptions{Kind: "quick", Pool: "p"})
	waitFor(t, func() bool {
		j, _ := q.Store().GetJob(ctx, quick)
		return j.Status == db.JobRunning || j.Status == db.JobSucceeded
	})

	r.Shutdown(200 * time.Millisecond)

	if j, _ := q.Store().GetJob(ctx, quick); j.Status != db.JobSucceeded || !quickDone.Load() {
		t.Fatalf("in-flight job not allowed to finish: %+v", j)
	}
	j, _ := q.Store().GetJob(ctx, stuck)
	if j.Status != db.JobQueued || j.Attempts != 0 || j.WorkerID != nil || j.LeaseUntil != nil {
		t.Fatalf("stuck job not released: %+v", j)
	}
	// No new claims after shutdown.
	q.Enqueue(ctx, nil, EnqueueOptions{Kind: "quick", Pool: "p", DedupeKey: "after"})
	time.Sleep(30 * time.Millisecond)
	if j, _ := q.Store().GetJobByDedupeKey(ctx, "after"); j.Status != db.JobQueued {
		t.Fatalf("job claimed after shutdown: %+v", j)
	}
}

func TestSchedulerOncePerBucket(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 11, 16, 12, 0, 10, 0, time.UTC)}
	q := New(dbtest.Open(t), c.Now)
	s := []Schedule{{Kind: "sweep", Pool: PoolMaintenance, Every: time.Minute}}
	q.ScheduleOnce(ctx, s, quietLog())
	c.Add(30 * time.Second)
	q.ScheduleOnce(ctx, s, quietLog())
	if n, _ := q.Store().CountJobs(ctx, PoolMaintenance, db.JobQueued); n != 1 {
		t.Fatalf("jobs in first bucket = %d", n)
	}
	c.Add(30 * time.Second)
	q.ScheduleOnce(ctx, s, quietLog())
	if n, _ := q.Store().CountJobs(ctx, PoolMaintenance, db.JobQueued); n != 2 {
		t.Fatalf("jobs after second bucket = %d", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

type metricsBuf struct{ strings.Builder }

func (m *metricsBuf) contains(s string) bool { return strings.Contains(m.String(), s) }
