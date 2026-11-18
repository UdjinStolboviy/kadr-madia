package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/ai"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/mcp"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
)

// AI operations (docs/AGENTS.md "AI moderation" and "Scheduled runs"):
//   - automatic moderation of new assets by a classifier (Claude vision or a
//     self-hosted model), per project;
//   - scheduled agent runs: Claude with the MCP tools of an agent token,
//     acting under that token's permissions, approvals and audit.

// Job kinds and pool.
const (
	JobAITick     = "ai.tick"
	JobAIModerate = "ai.moderate"
	JobAgentRun   = "ai.agent_run"
	PoolAI        = "ai"
)

const (
	settingModerationPrefix = "ai.moderation."
	moderationSweepBatch    = 50
	maxBacklog              = 1000
	maxPromptLen            = 20000
	defaultPolicy           = "No sexual content or nudity, no graphic violence or gore, no hate symbols, no visible personal data (ID documents, bank cards, addresses), no spam or advertising overlays, no content involving minors in unsafe situations."
)

// moderationSettings is the per-project automatic moderation config.
type moderationSettings struct {
	Enabled bool     `json:"enabled"`
	Policy  string   `json:"policy"`
	Mode    string   `json:"mode"`  // flag_only: approve or flag; decide: approve, flag or reject
	Kinds   []string `json:"kinds"` // image | video | file
	Since   string   `json:"since"` // RFC 3339: assets created before are left alone (use the backlog action)
}

func (s *Server) aiAvailable() bool { return s.cfg.Classifier != nil }

func (s *Server) loadModerationSettings(ctx context.Context, projectID string) (moderationSettings, error) {
	st := moderationSettings{Policy: defaultPolicy, Mode: "flag_only", Kinds: []string{"image", "video"}}
	err := s.store.GetSetting(ctx, settingModerationPrefix+projectID, &st)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return st, err
	}
	return st, nil
}

// handleGetModerationSettings: GET /projects/{id}/moderation/settings
func (s *Server) handleGetModerationSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	st, err := s.loadModerationSettings(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"settings": st, "ai": s.aiStatus()})
}

// handlePutModerationSettings: PUT /projects/{id}/moderation/settings
func (s *Server) handlePutModerationSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var in moderationSettings
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	errs := map[string]string{}
	in.Policy = strings.TrimSpace(in.Policy)
	if in.Policy == "" {
		in.Policy = defaultPolicy
	}
	if len([]rune(in.Policy)) > 4000 {
		errs["policy"] = "at most 4000 characters"
	}
	if in.Mode == "" {
		in.Mode = "flag_only"
	}
	if in.Mode != "flag_only" && in.Mode != "decide" {
		errs["mode"] = `must be "flag_only" or "decide"`
	}
	kinds := []string{}
	for _, k := range in.Kinds {
		if !slices.Contains([]string{"image", "video", "file"}, k) {
			errs["kinds"] = "image, video or file"
		} else if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		kinds = []string{"image", "video"}
	}
	in.Kinds = kinds
	if in.Enabled && !s.aiAvailable() {
		errs["enabled"] = "AI is not configured on this server (set ANTHROPIC_API_KEY or KADR_AI_CLASSIFIER_URL)"
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	prev, err := s.loadModerationSettings(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	in.Since = prev.Since
	if in.Enabled && (!prev.Enabled || in.Since == "") {
		in.Since = s.now().UTC().Format(time.RFC3339)
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SetSetting(r.Context(), settingModerationPrefix+p.ID, in, s.now()); err != nil {
			return err
		}
		return s.audit(r, tx, "moderation.settings", &p.ID, &p.ID, map[string]any{"enabled": in.Enabled, "mode": in.Mode, "kinds": in.Kinds})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"settings": in, "ai": s.aiStatus()})
}

// handleModerationBacklog: POST /projects/{id}/moderation/backlog — queue every
// unreviewed asset (up to 1000) for the AI classifier now.
func (s *Server) handleModerationBacklog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !s.aiAvailable() || s.cfg.Queue == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "ai_unavailable", "AI is not configured on this server")
		return
	}
	st, err := s.loadModerationSettings(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	n, err := s.enqueueModeration(r.Context(), p.ID, st, nil, maxBacklog)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "moderation.backlog", &p.ID, &p.ID, map[string]any{"queued": n})
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": n})
}

func (s *Server) aiStatus() map[string]any {
	st := map[string]any{"available": s.aiAvailable(), "classifier": nil, "agent_runs": s.cfg.AI != nil, "model": nil}
	if s.cfg.Classifier != nil {
		st["classifier"] = s.cfg.Classifier.Name()
	}
	if s.cfg.AI != nil {
		st["model"] = s.cfg.AI.Model
	}
	return st
}

// handleAIStatus: GET /ai/status
func (s *Server) handleAIStatus(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.aiStatus())
}

// enqueueModeration queues unreviewed ready assets of a project. With since,
// only assets created after it.
func (s *Server) enqueueModeration(ctx context.Context, projectID string, st moderationSettings, since *time.Time, limit int) (int, error) {
	queued, cursor := 0, ""
	for queued < limit {
		items, err := s.store.ListModerationQueue(ctx, db.ModerationFilter{ProjectID: projectID, Status: "unreviewed", Cursor: cursor, Limit: 100})
		if err != nil {
			return queued, err
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			if since != nil && it.CreatedAt.Before(*since) {
				return queued, nil // newest first: everything after is older
			}
			if !slices.Contains(st.Kinds, it.Kind) {
				continue
			}
			_, created, err := s.cfg.Queue.Enqueue(ctx, nil, queue.EnqueueOptions{Kind: JobAIModerate, Pool: PoolAI, MaxAttempts: 3,
				Payload: map[string]string{"project_id": projectID, "asset_id": it.AssetID}, DedupeKey: "ai.moderate:" + it.AssetID})
			if err != nil {
				return queued, err
			}
			if created {
				queued++
			}
			if queued >= limit {
				break
			}
		}
		cursor = items[len(items)-1].AssetID
	}
	return queued, nil
}

// ───────────── jobs ─────────────

// RegisterJobs installs the AI job handlers on a runner.
func (s *Server) RegisterJobs(r *queue.Runner) {
	r.Handle(JobAITick, queue.Handler{Run: s.runAITick, Lease: 2 * time.Minute})
	r.Handle(JobAIModerate, queue.Handler{Run: s.runAIModerate, Lease: 3 * time.Minute})
	r.Handle(JobAgentRun, queue.Handler{Run: s.runAgentJob, Dead: s.agentRunDead, Lease: 5 * time.Minute})
}

// Schedules returns the periodic AI job.
func Schedules() []queue.Schedule {
	return []queue.Schedule{{Kind: JobAITick, Pool: queue.PoolMaintenance, Every: time.Minute}}
}

// runAITick queues new assets for automatic moderation and fires due agent schedules.
func (s *Server) runAITick(ctx context.Context, _ *queue.Job) error {
	if s.aiAvailable() {
		all, err := s.store.ListSettingsWithPrefix(ctx, settingModerationPrefix)
		if err != nil {
			return err
		}
		for key, raw := range all {
			var st moderationSettings
			if json.Unmarshal([]byte(raw), &st) != nil || !st.Enabled {
				continue
			}
			var since *time.Time
			if t, err := time.Parse(time.RFC3339, st.Since); err == nil {
				since = &t
			}
			if _, err := s.enqueueModeration(ctx, strings.TrimPrefix(key, settingModerationPrefix), st, since, moderationSweepBatch); err != nil {
				s.log.WarnContext(ctx, "ai moderation sweep", "project_id", strings.TrimPrefix(key, settingModerationPrefix), "err", err)
			}
		}
	}
	now := s.now()
	_ = s.store.FailStaleAgentRuns(ctx, now.Add(-2*time.Hour))
	due, err := s.store.DueAgentSchedules(ctx, now)
	if err != nil {
		return err
	}
	for _, sc := range due {
		next := nextRun(sc, now)
		if err := s.store.AdvanceAgentSchedule(ctx, sc.ID, now, &next); err != nil {
			return err
		}
		if _, err := s.startRun(ctx, sc.TokenID, sc.Prompt, &sc.ID, "schedule", nil); err != nil {
			s.log.WarnContext(ctx, "start scheduled agent run", "schedule_id", sc.ID, "err", err)
		}
	}
	return nil
}

func (s *Server) runAIModerate(ctx context.Context, j *queue.Job) error {
	var in struct{ ProjectID, AssetID string }
	if err := json.Unmarshal(j.Payload, &struct {
		P *string `json:"project_id"`
		A *string `json:"asset_id"`
	}{&in.ProjectID, &in.AssetID}); err != nil {
		return queue.Permanent(err)
	}
	if s.cfg.Classifier == nil || s.cfg.Pipeline == nil {
		return queue.Permanent(errors.New("AI classifier is not configured"))
	}
	st, err := s.loadModerationSettings(ctx, in.ProjectID)
	if err != nil {
		return err
	}
	if _, err := s.store.GetModeration(ctx, in.AssetID); err == nil {
		return nil // a human or another job already decided
	}
	a, err := s.store.GetAsset(ctx, in.AssetID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && a.Status != db.AssetReady) {
		return nil
	}
	if err != nil {
		return err
	}
	pv, err := s.cfg.Pipeline.ReadPreview(ctx, in.ProjectID, in.AssetID, 1024, previewMaxBytes)
	if err != nil {
		var pe *pipeline.Error
		if errors.As(err, &pe) && pe.Status < 500 {
			return nil // nothing to look at (no image rendition, not ready, gone)
		}
		return err
	}
	start := time.Now()
	d, err := s.cfg.Classifier.Classify(ctx, pv.Data, "image/"+pv.Format, st.Policy)
	s.cfg.Metrics.GetOrCreateHistogram("kadr_ai_moderation_seconds").UpdateDuration(start)
	if err != nil {
		s.cfg.Metrics.GetOrCreateCounter("kadr_ai_moderation_errors_total").Inc()
		return err
	}
	if st.Mode == "flag_only" && d.Status == db.ModerationRejected {
		d.Status = db.ModerationFlagged
	}
	note := strings.TrimSpace(d.Note)
	actor := s.cfg.Classifier.Name()
	m := &db.AssetModeration{AssetID: a.ID, ProjectID: a.ProjectID, Status: d.Status, Labels: d.Labels, ActorType: db.ActorSystem,
		ActorID: &actor, UpdatedAt: s.now()}
	if note != "" {
		m.Note = &note
	}
	err = s.saveModeration(ctx, m, a.NS, func(tx *db.Store, prev string) error {
		return tx.InsertAudit(ctx, &db.AuditEntry{ActorType: db.ActorSystem, ActorID: &actor, ProjectID: &a.ProjectID,
			Action: "asset.moderate", Target: &a.ID, CreatedAt: s.now()},
			map[string]any{"status": m.Status, "previous_status": prev, "labels": m.Labels, "confidence": d.Confidence, "classifier": actor})
	})
	if err == nil {
		s.cfg.Metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_ai_moderations_total{status=%q}`, m.Status)).Inc()
	}
	return err
}

// ───────────── agent runs ─────────────

func (s *Server) startRun(ctx context.Context, tokenID, prompt string, scheduleID *string, trigger string, by *string) (*db.AgentRun, error) {
	if s.cfg.AI == nil || s.cfg.Queue == nil {
		return nil, errors.New("agent runs need ANTHROPIC_API_KEY")
	}
	run := &db.AgentRun{ID: db.NewID(db.PrefixAgentRun), ScheduleID: scheduleID, TokenID: tokenID, Trigger: trigger, Prompt: prompt,
		Status: "queued", CreatedBy: by, CreatedAt: s.now()}
	if err := s.store.CreateAgentRun(ctx, run); err != nil {
		return nil, err
	}
	_, _, err := s.cfg.Queue.Enqueue(ctx, nil, queue.EnqueueOptions{Kind: JobAgentRun, Pool: PoolAI, MaxAttempts: 1,
		Payload: map[string]string{"run_id": run.ID}, DedupeKey: "ai.agent_run:" + run.ID})
	return run, err
}

const unattendedNote = `

This is an unattended run started by Kadr (scheduled or by an administrator). Nobody can answer questions during the run.
Work autonomously within your permissions; actions that need approval will wait for a human — list them in the report.
Finish with a concise report in Markdown: what you checked, what you changed (with ids), what needs a human, and numbers where relevant.`

func (s *Server) runAgentJob(ctx context.Context, j *queue.Job) error {
	var in struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(j.Payload, &in); err != nil {
		return queue.Permanent(err)
	}
	run, err := s.store.GetAgentRun(ctx, in.RunID)
	if err != nil {
		return queue.Permanent(err)
	}
	finish := func(status string, out *ai.RunResult, runErr error) error {
		run.Status = status
		if out != nil {
			if out.Output != "" {
				run.Output = &out.Output
			}
			run.ToolCalls, run.InputTokens, run.OutputTokens = out.ToolCalls, out.InputTokens, out.OutputTokens
		}
		if runErr != nil {
			msg := runErr.Error()
			run.Error = &msg
		}
		if err := s.store.FinishAgentRun(ctx, run, s.now()); err != nil {
			return err
		}
		s.notifyRun(run)
		return nil
	}
	t, err := s.store.GetAgentToken(ctx, run.TokenID)
	if err != nil {
		return finish("failed", nil, fmt.Errorf("agent token: %w", err))
	}
	enabled, err := s.agentsEnabled(ctx)
	if err != nil {
		return err
	}
	switch {
	case !enabled:
		return finish("failed", nil, errors.New("agent access is switched off"))
	case !t.Active(s.now()):
		return finish("failed", nil, errors.New("the agent token is revoked or expired"))
	case s.cfg.AI == nil:
		return finish("failed", nil, errors.New("ANTHROPIC_API_KEY is not configured"))
	}
	maxTurns := 30
	if run.ScheduleID != nil {
		if sc, err := s.store.GetAgentSchedule(ctx, *run.ScheduleID); err == nil {
			maxTurns = sc.MaxTurns
		}
	}
	if err := s.store.StartAgentRun(ctx, run.ID, s.cfg.AI.Model, s.now()); err != nil {
		return err
	}
	actx := context.WithValue(ctx, ctxAgent, t)
	actx = context.WithValue(actx, ctxInternal, true)
	actx = context.WithValue(actx, ctxClientIP, "127.0.0.1")
	cat := s.mcpCatalog(t)
	tools := make([]ai.Tool, 0, len(cat.Tools))
	for _, mt := range cat.Tools {
		mt := mt
		tools = append(tools, ai.Tool{Name: mt.Name, Description: mt.Description, Schema: mt.InputSchema,
			Call: func(ctx context.Context, args map[string]any) ai.ToolOutput {
				res, err := mt.Call(context.WithValue(actx, ctxRunID, run.ID), args)
				if err != nil {
					return ai.ToolOutput{Text: err.Error(), IsError: true}
				}
				return toolOutput(res)
			}})
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	out, err := s.cfg.AI.RunAgent(rctx, cat.Instructions+unattendedNote, run.Prompt, tools, maxTurns)
	_ = s.store.InsertAudit(ctx, &db.AuditEntry{ActorType: db.ActorAgent, ActorID: &t.ID, Action: "agent.run", Target: &run.ID, CreatedAt: s.now()},
		map[string]any{"trigger": run.Trigger, "schedule_id": run.ScheduleID, "tool_calls": out.ToolCalls, "error": errString(err)})
	s.cfg.Metrics.GetOrCreateCounter(`kadr_ai_agent_runs_total{status="` + map[bool]string{true: "failed", false: "succeeded"}[err != nil] + `"}`).Inc()
	if err != nil {
		return finish("failed", out, err)
	}
	return finish("succeeded", out, nil)
}

const ctxRunID ctxKey = iota + 400

func errString(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func toolOutput(res *mcp.ToolResult) ai.ToolOutput {
	var out ai.ToolOutput
	var sb strings.Builder
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		case "image":
			out.Images = append(out.Images, ai.Image{MimeType: c.MimeType, Data: c.Data})
		}
	}
	out.Text, out.IsError = strings.TrimSpace(sb.String()), res.IsError
	return out
}

func (s *Server) agentRunDead(ctx context.Context, j *queue.Job, cause error) {
	var in struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(j.Payload, &in) != nil {
		return
	}
	if run, err := s.store.GetAgentRun(ctx, in.RunID); err == nil && run.FinishedAt == nil {
		msg := cause.Error()
		run.Status, run.Error = "failed", &msg
		_ = s.store.FinishAgentRun(ctx, run, s.now())
	}
}

func (s *Server) notifyRun(run *db.AgentRun) {
	if s.cfg.Notifier == nil {
		return
	}
	if run.ScheduleID != nil {
		if sc, err := s.store.GetAgentSchedule(context.Background(), *run.ScheduleID); err == nil && !sc.Notify {
			return
		}
	}
	name := run.TokenID
	if t, err := s.store.GetAgentToken(context.Background(), run.TokenID); err == nil {
		name = t.Name
	}
	text := fmt.Sprintf("🤖 Kadr agent %q: run %s\n", name, run.Status)
	if run.Output != nil {
		text += "\n" + truncateRunes(*run.Output, 3300)
	}
	if run.Error != nil {
		text += "\nError: " + truncateRunes(*run.Error, 400)
	}
	if s.cfg.PublicURL != "" {
		text += "\n" + strings.TrimRight(s.cfg.PublicURL, "/") + "/agents?tab=runs"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.cfg.Notifier.Notify(ctx, text); err != nil {
			s.log.Warn("agent run notification failed", "err", err)
		}
	}()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ───────────── schedules API ─────────────

var hhmmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// nextRun returns the first firing strictly after now.
func nextRun(sc *db.AgentSchedule, now time.Time) time.Time {
	now = now.UTC()
	h, _ := strconv.Atoi(sc.TimeUTC[:2])
	m, _ := strconv.Atoi(sc.TimeUTC[3:])
	switch sc.Frequency {
	case "hourly":
		t := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), m, 0, 0, time.UTC)
		if !t.After(now) {
			t = t.Add(time.Hour)
		}
		return t
	case "weekly":
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.UTC)
		t = t.AddDate(0, 0, (sc.Weekday-int(t.Weekday())+7)%7)
		if !t.After(now) {
			t = t.AddDate(0, 0, 7)
		}
		return t
	default: // daily
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.UTC)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
}

type scheduleDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	AgentID   string  `json:"agent_id"`
	AgentName string  `json:"agent_name"`
	Prompt    string  `json:"prompt"`
	Frequency string  `json:"frequency"`
	TimeUTC   string  `json:"time_utc"`
	Weekday   int     `json:"weekday"`
	MaxTurns  int     `json:"max_turns"`
	Notify    bool    `json:"notify"`
	Enabled   bool    `json:"enabled"`
	LastRunAt *string `json:"last_run_at"`
	NextRunAt *string `json:"next_run_at"`
	CreatedAt string  `json:"created_at"`
}

func toScheduleDTO(sc *db.AgentSchedule, names map[string]string) scheduleDTO {
	return scheduleDTO{ID: sc.ID, Name: sc.Name, AgentID: sc.TokenID, AgentName: names[sc.TokenID], Prompt: sc.Prompt, Frequency: sc.Frequency,
		TimeUTC: sc.TimeUTC, Weekday: sc.Weekday, MaxTurns: sc.MaxTurns, Notify: sc.Notify, Enabled: sc.Enabled,
		LastRunAt: tsp(sc.LastRunAt), NextRunAt: tsp(sc.NextRunAt), CreatedAt: ts(sc.CreatedAt)}
}

type runDTO struct {
	ID           string  `json:"id"`
	ScheduleID   *string `json:"schedule_id"`
	AgentID      string  `json:"agent_id"`
	AgentName    string  `json:"agent_name"`
	Trigger      string  `json:"trigger"`
	Prompt       string  `json:"prompt"`
	Status       string  `json:"status"`
	Output       *string `json:"output"`
	Error        *string `json:"error"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Model        *string `json:"model"`
	CreatedAt    string  `json:"created_at"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
}

func toRunDTO(r *db.AgentRun, names map[string]string) runDTO {
	return runDTO{ID: r.ID, ScheduleID: r.ScheduleID, AgentID: r.TokenID, AgentName: names[r.TokenID], Trigger: r.Trigger, Prompt: r.Prompt,
		Status: r.Status, Output: r.Output, Error: r.Error, ToolCalls: r.ToolCalls, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		Model: r.Model, CreatedAt: ts(r.CreatedAt), StartedAt: tsp(r.StartedAt), FinishedAt: tsp(r.FinishedAt)}
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	scs, err := s.store.ListAgentSchedules(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names := s.agentNames(r.Context())
	out := make([]scheduleDTO, 0, len(scs))
	for _, sc := range scs {
		out = append(out, toScheduleDTO(sc, names))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "ai": s.aiStatus()})
}

type scheduleInput struct {
	Name      *string `json:"name"`
	AgentID   *string `json:"agent_id"`
	Prompt    *string `json:"prompt"`
	Frequency *string `json:"frequency"`
	TimeUTC   *string `json:"time_utc"`
	Weekday   *int    `json:"weekday"`
	MaxTurns  *int    `json:"max_turns"`
	Notify    *bool   `json:"notify"`
	Enabled   *bool   `json:"enabled"`
}

func (s *Server) applyScheduleInput(ctx context.Context, in *scheduleInput, sc *db.AgentSchedule) map[string]string {
	errs := map[string]string{}
	if in.Name != nil {
		sc.Name = strings.TrimSpace(*in.Name)
	}
	if sc.Name == "" || len([]rune(sc.Name)) > 100 {
		errs["name"] = "required, at most 100 characters"
	}
	if in.AgentID != nil {
		sc.TokenID = *in.AgentID
	}
	if t, err := s.store.GetAgentToken(ctx, sc.TokenID); err != nil || t.RevokedAt != nil {
		errs["agent_id"] = "choose an active agent token; the run gets exactly its permissions"
	}
	if in.Prompt != nil {
		sc.Prompt = strings.TrimSpace(*in.Prompt)
	}
	if sc.Prompt == "" || len([]rune(sc.Prompt)) > maxPromptLen {
		errs["prompt"] = "required, at most 20000 characters"
	}
	if in.Frequency != nil {
		sc.Frequency = *in.Frequency
	}
	if !slices.Contains([]string{"hourly", "daily", "weekly"}, sc.Frequency) {
		errs["frequency"] = "hourly, daily or weekly"
	}
	if in.TimeUTC != nil {
		sc.TimeUTC = *in.TimeUTC
	}
	if !hhmmRe.MatchString(sc.TimeUTC) {
		errs["time_utc"] = "HH:MM (UTC)"
	}
	if in.Weekday != nil {
		sc.Weekday = *in.Weekday
	}
	if sc.Weekday < 0 || sc.Weekday > 6 {
		errs["weekday"] = "0 (Sunday) to 6"
	}
	if in.MaxTurns != nil {
		sc.MaxTurns = *in.MaxTurns
	}
	if sc.MaxTurns < 1 || sc.MaxTurns > 100 {
		errs["max_turns"] = "1 to 100"
	}
	if in.Notify != nil {
		sc.Notify = *in.Notify
	}
	if in.Enabled != nil {
		sc.Enabled = *in.Enabled
	}
	return errs
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var in scheduleInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	now := s.now()
	sc := &db.AgentSchedule{ID: db.NewID(db.PrefixAgentSchedule), Frequency: "daily", TimeUTC: "03:00", Weekday: 1, MaxTurns: 30,
		Notify: true, Enabled: true, CreatedBy: adminFrom(r.Context()).ID, CreatedAt: now, UpdatedAt: now}
	if errs := s.applyScheduleInput(r.Context(), &in, sc); len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	s.saveSchedule(w, r, sc, "agent.schedule.create", http.StatusCreated)
}

func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request, sc *db.AgentSchedule, action string, status int) {
	next := nextRun(sc, s.now())
	sc.NextRunAt = &next
	if !sc.Enabled {
		sc.NextRunAt = nil
	}
	sc.UpdatedAt = s.now()
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SaveAgentSchedule(r.Context(), sc); err != nil {
			return err
		}
		return s.audit(r, tx, action, nil, &sc.ID, map[string]any{"name": sc.Name, "agent_id": sc.TokenID, "frequency": sc.Frequency,
			"time_utc": sc.TimeUTC, "enabled": sc.Enabled})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, toScheduleDTO(sc, s.agentNames(r.Context())))
}

func (s *Server) loadSchedule(w http.ResponseWriter, r *http.Request) (*db.AgentSchedule, bool) {
	sc, err := s.store.GetAgentSchedule(r.Context(), chi.URLParam(r, "schedule_id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "schedule")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return sc, true
}

func (s *Server) handlePatchSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.loadSchedule(w, r)
	if !ok {
		return
	}
	var in scheduleInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	if errs := s.applyScheduleInput(r.Context(), &in, sc); len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	s.saveSchedule(w, r, sc, "agent.schedule.update", http.StatusOK)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.loadSchedule(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAgentSchedule(r.Context(), sc.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "agent.schedule.delete", nil, &sc.ID, map[string]any{"name": sc.Name})
	w.WriteHeader(http.StatusNoContent)
}

// handleRunSchedule: POST /agent-schedules/{schedule_id}/run — run now.
func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.loadSchedule(w, r)
	if !ok {
		return
	}
	s.startRunHTTP(w, r, sc.TokenID, sc.Prompt, &sc.ID)
}

// handleCreateRun: POST /agent-runs {agent_id, prompt} — an ad-hoc run.
func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID string `json:"agent_id"`
		Prompt  string `json:"prompt"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	errs := map[string]string{}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" || len([]rune(in.Prompt)) > maxPromptLen {
		errs["prompt"] = "required, at most 20000 characters"
	}
	if t, err := s.store.GetAgentToken(r.Context(), in.AgentID); err != nil || !t.Active(s.now()) {
		errs["agent_id"] = "choose an active agent token"
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	s.startRunHTTP(w, r, in.AgentID, in.Prompt, nil)
}

func (s *Server) startRunHTTP(w http.ResponseWriter, r *http.Request, tokenID, prompt string, scheduleID *string) {
	if s.cfg.AI == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "ai_unavailable", "agent runs need ANTHROPIC_API_KEY on the server")
		return
	}
	by := adminFrom(r.Context()).ID
	run, err := s.startRun(r.Context(), tokenID, prompt, scheduleID, "manual", &by)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "agent.run.start", nil, &run.ID, map[string]any{"agent_id": tokenID, "schedule_id": scheduleID})
	httpx.WriteJSON(w, http.StatusAccepted, toRunDTO(run, s.agentNames(r.Context())))
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListAgentRuns(r.Context(), r.URL.Query().Get("schedule_id"), atoiDefault(r.URL.Query().Get("limit"), 50))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names := s.agentNames(r.Context())
	out := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunDTO(run, names))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[runDTO]{Items: out})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetAgentRun(r.Context(), chi.URLParam(r, "run_id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "run")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRunDTO(run, s.agentNames(r.Context())))
}
