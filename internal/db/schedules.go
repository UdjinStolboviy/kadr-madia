package db

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ID prefixes of scheduled agent runs.
const (
	PrefixAgentSchedule = "ags"
	PrefixAgentRun      = "agr"
)

// AgentSchedule runs an agent prompt periodically.
type AgentSchedule struct {
	ID        string
	Name      string
	TokenID   string
	Prompt    string
	Frequency string // hourly | daily | weekly
	TimeUTC   string // HH:MM
	Weekday   int
	MaxTurns  int
	Notify    bool
	Enabled   bool
	CreatedBy string
	LastRunAt *time.Time
	NextRunAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

const scheduleCols = `id, name, token_id, prompt, frequency, time_utc, weekday, max_turns, notify, enabled, created_by,
	last_run_at, next_run_at, created_at, updated_at`

func scanSchedule(row interface{ Scan(...any) error }) (*AgentSchedule, error) {
	var a AgentSchedule
	var last, next sql.NullInt64
	var created, updated int64
	if err := row.Scan(&a.ID, &a.Name, &a.TokenID, &a.Prompt, &a.Frequency, &a.TimeUTC, &a.Weekday, &a.MaxTurns, &a.Notify, &a.Enabled,
		&a.CreatedBy, &last, &next, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	a.LastRunAt, a.NextRunAt = fromNullMs(last), fromNullMs(next)
	a.CreatedAt, a.UpdatedAt = fromMs(created), fromMs(updated)
	return &a, nil
}

// SaveAgentSchedule inserts or replaces a schedule.
func (s *Store) SaveAgentSchedule(ctx context.Context, a *AgentSchedule) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO agent_schedules (`+scheduleCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, token_id = excluded.token_id, prompt = excluded.prompt,
		   frequency = excluded.frequency, time_utc = excluded.time_utc, weekday = excluded.weekday, max_turns = excluded.max_turns,
		   notify = excluded.notify, enabled = excluded.enabled, next_run_at = excluded.next_run_at, updated_at = excluded.updated_at`,
		a.ID, a.Name, a.TokenID, a.Prompt, a.Frequency, a.TimeUTC, a.Weekday, a.MaxTurns, a.Notify, a.Enabled, a.CreatedBy,
		nullMs(a.LastRunAt), nullMs(a.NextRunAt), ms(a.CreatedAt), ms(a.UpdatedAt))
	return mapErr(err)
}

// GetAgentSchedule returns a schedule.
func (s *Store) GetAgentSchedule(ctx context.Context, id string) (*AgentSchedule, error) {
	return scanSchedule(s.r.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM agent_schedules WHERE id = ?`, id))
}

// ListAgentSchedules returns every schedule, newest first.
func (s *Store) ListAgentSchedules(ctx context.Context) ([]*AgentSchedule, error) {
	return s.querySchedules(ctx, `SELECT `+scheduleCols+` FROM agent_schedules ORDER BY created_at DESC`)
}

// DueAgentSchedules returns enabled schedules whose next run is due.
func (s *Store) DueAgentSchedules(ctx context.Context, now time.Time) ([]*AgentSchedule, error) {
	return s.querySchedules(ctx, `SELECT `+scheduleCols+` FROM agent_schedules WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?`, ms(now))
}

func (s *Store) querySchedules(ctx context.Context, q string, args ...any) ([]*AgentSchedule, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*AgentSchedule{}
	for rows.Next() {
		a, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// AdvanceAgentSchedule records a firing and the next due time.
func (s *Store) AdvanceAgentSchedule(ctx context.Context, id string, ran time.Time, next *time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE agent_schedules SET last_run_at = ?, next_run_at = ? WHERE id = ?`, ms(ran), nullMs(next), id)
	return mapErr(err)
}

// DeleteAgentSchedule removes a schedule (its runs stay).
func (s *Store) DeleteAgentSchedule(ctx context.Context, id string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM agent_schedules WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}

// AgentRun is one execution of an agent prompt.
type AgentRun struct {
	ID           string
	ScheduleID   *string
	TokenID      string
	Trigger      string
	Prompt       string
	Status       string
	Output       *string
	Error        *string
	ToolCalls    int
	InputTokens  int64
	OutputTokens int64
	Model        *string
	CreatedBy    *string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

const runCols = `id, schedule_id, token_id, trigger, prompt, status, output, error, tool_calls, input_tokens, output_tokens, model,
	created_by, created_at, started_at, finished_at`

func scanRun(row interface{ Scan(...any) error }) (*AgentRun, error) {
	var r AgentRun
	var sched, out, errS, model, by sql.NullString
	var started, finished sql.NullInt64
	var created int64
	if err := row.Scan(&r.ID, &sched, &r.TokenID, &r.Trigger, &r.Prompt, &r.Status, &out, &errS, &r.ToolCalls, &r.InputTokens,
		&r.OutputTokens, &model, &by, &created, &started, &finished); err != nil {
		return nil, mapErr(err)
	}
	r.ScheduleID, r.Output, r.Error, r.Model, r.CreatedBy = fromNullStr(sched), fromNullStr(out), fromNullStr(errS), fromNullStr(model), fromNullStr(by)
	r.CreatedAt, r.StartedAt, r.FinishedAt = fromMs(created), fromNullMs(started), fromNullMs(finished)
	return &r, nil
}

// CreateAgentRun inserts a queued run.
func (s *Store) CreateAgentRun(ctx context.Context, r *AgentRun) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO agent_runs (id, schedule_id, token_id, trigger, prompt, status, created_by, created_at) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?)`,
		r.ID, nullStr(r.ScheduleID), r.TokenID, r.Trigger, r.Prompt, nullStr(r.CreatedBy), ms(r.CreatedAt))
	return mapErr(err)
}

// GetAgentRun returns a run.
func (s *Store) GetAgentRun(ctx context.Context, id string) (*AgentRun, error) {
	return scanRun(s.r.QueryRowContext(ctx, `SELECT `+runCols+` FROM agent_runs WHERE id = ?`, id))
}

// StartAgentRun marks a run running.
func (s *Store) StartAgentRun(ctx context.Context, id, model string, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE agent_runs SET status = 'running', model = ?, started_at = ? WHERE id = ?`, model, ms(now), id)
	return mapErr(err)
}

// FinishAgentRun stores the outcome of a run.
func (s *Store) FinishAgentRun(ctx context.Context, r *AgentRun, now time.Time) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE agent_runs SET status = ?, output = ?, error = ?, tool_calls = ?, input_tokens = ?, output_tokens = ?, finished_at = ? WHERE id = ?`,
		r.Status, nullStr(r.Output), nullStr(r.Error), r.ToolCalls, r.InputTokens, r.OutputTokens, ms(now), r.ID)
	return mapErr(err)
}

// ListAgentRuns returns runs newest first, optionally for one schedule.
func (s *Store) ListAgentRuns(ctx context.Context, scheduleID string, limit int) ([]*AgentRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + runCols + ` FROM agent_runs`
	args := []any{}
	if scheduleID != "" {
		q += ` WHERE schedule_id = ?`
		args = append(args, scheduleID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*AgentRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// FailStaleAgentRuns marks runs left running by a crash as failed.
func (s *Store) FailStaleAgentRuns(ctx context.Context, olderThan time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE agent_runs SET status = 'failed', error = 'interrupted (server restart)', finished_at = ?
		WHERE status = 'running' AND started_at < ?`, ms(time.Now()), ms(olderThan))
	return mapErr(err)
}

// ListSettingsWithPrefix returns raw JSON settings whose key starts with prefix.
func (s *Store) ListSettingsWithPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT key, value_json FROM settings WHERE key >= ? AND key < ?`, prefix, prefix+"￿")
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, mapErr(err)
		}
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, mapErr(rows.Err())
}
