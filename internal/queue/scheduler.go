package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Schedule is a periodic job. Each interval bucket enqueues at most one job
// thanks to the dedupe key "<kind>@<bucket start unix ms>".
type Schedule struct {
	Kind    string
	Pool    string
	Every   time.Duration
	Payload any
}

// RunScheduler enqueues every schedule once per interval until ctx is done.
// It ticks immediately at start.
func (q *Queue) RunScheduler(ctx context.Context, schedules []Schedule, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	if len(schedules) == 0 {
		return
	}
	tick := schedules[0].Every
	for _, s := range schedules {
		tick = min(tick, s.Every)
	}
	tick = min(max(tick/2, time.Second), 30*time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		q.ScheduleOnce(ctx, schedules, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ScheduleOnce enqueues the job of the current bucket of every schedule.
func (q *Queue) ScheduleOnce(ctx context.Context, schedules []Schedule, log *slog.Logger) {
	now := q.now()
	for _, s := range schedules {
		bucket := now.Truncate(s.Every).UnixMilli()
		_, created, err := q.Enqueue(ctx, nil, EnqueueOptions{
			Kind: s.Kind, Pool: s.Pool, Payload: s.Payload, MaxAttempts: 1,
			DedupeKey: fmt.Sprintf("%s@%d", s.Kind, bucket),
		})
		if err != nil {
			if ctx.Err() == nil && log != nil {
				log.Error("schedule job", "kind", s.Kind, "err", err)
			}
			continue
		}
		if created && log != nil {
			log.Debug("scheduled job", "kind", s.Kind)
		}
	}
}
