package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/UdjinStolboviy/kadr-madia/internal/ai"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
)

type fakeClassifier struct{ d ai.Decision }

func (f *fakeClassifier) Name() string { return "test:classifier" }
func (f *fakeClassifier) Classify(_ context.Context, img []byte, mime, policy string) (*ai.Decision, error) {
	if mime != "image/webp" || len(img) == 0 || policy == "" {
		return nil, io.ErrUnexpectedEOF
	}
	d := f.d
	return &d, nil
}

type fakePipeline struct{ AssetService }

func (fakePipeline) ReadPreview(_ context.Context, _, _ string, _, _ int64) (*pipeline.Preview, error) {
	return &pipeline.Preview{Name: "main", Format: "webp", Data: []byte("RIFFxxxxWEBP")}, nil
}

func TestAIModeration(t *testing.T) {
	cls := &fakeClassifier{d: ai.Decision{Status: "rejected", Labels: []string{"nsfw"}, Note: "explicit", Confidence: 0.9}}
	e := newEnvDev(t, nil, true, func(c *Config) {
		c.Classifier = cls
		c.Pipeline = fakePipeline{}
		c.Queue = queue.New(c.Store, nil)
	})
	c := &client{e: e}
	c.login(t, nil)
	pid := c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme"}, true).json(t)["id"].(string)
	ctx := context.Background()
	w := e.store.DB().W
	w.ExecContext(ctx, `INSERT INTO assets (id, project_id, ns, kind, visibility, status, created_at) VALUES ('ast_01old', ?, 'u', 'image', 'public', 'ready', 1)`, pid)

	r := c.do("PUT", "/admin/api/projects/"+pid+"/moderation/settings", map[string]any{"enabled": true, "mode": "flag_only"}, true)
	if r.code != 200 {
		t.Fatalf("settings: %d %s", r.code, r.body)
	}
	st := r.json(t)["settings"].(map[string]any)
	if st["since"] == "" || !strings.Contains(st["policy"].(string), "nudity") {
		t.Fatalf("settings: %s", r.body)
	}
	// Assets created after enabling are picked up by the tick; older ones are not.
	now := time.Now().Add(time.Minute).UnixMilli()
	w.ExecContext(ctx, `INSERT INTO assets (id, project_id, ns, kind, visibility, status, created_at) VALUES ('ast_02new', ?, 'u', 'image', 'public', 'ready', ?)`, pid, now)
	if err := e.srv.runAITick(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	e.store.DB().R.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind = ?`, JobAIModerate).Scan(&n)
	if n != 1 {
		t.Fatalf("queued moderation jobs = %d, want 1", n)
	}
	job := &queue.Job{Payload: json.RawMessage(`{"project_id":"` + pid + `","asset_id":"ast_02new"}`)}
	if err := e.srv.runAIModerate(ctx, job); err != nil {
		t.Fatal(err)
	}
	m, err := e.store.GetModeration(ctx, "ast_02new")
	if err != nil || m.Status != "flagged" || m.ActorType != db.ActorSystem || *m.ActorID != "test:classifier" {
		t.Fatalf("flag_only must turn rejected into flagged: %+v %v", m, err)
	}
	// Decide mode rejects; the backlog picks up old assets.
	c.do("PUT", "/admin/api/projects/"+pid+"/moderation/settings", map[string]any{"enabled": true, "mode": "decide"}, true)
	if r := c.do("POST", "/admin/api/projects/"+pid+"/moderation/backlog", nil, true); r.code != 202 || r.json(t)["queued"].(float64) != 1 {
		t.Fatalf("backlog: %d %s", r.code, r.body)
	}
	job.Payload = json.RawMessage(`{"project_id":"` + pid + `","asset_id":"ast_01old"}`)
	if err := e.srv.runAIModerate(ctx, job); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.store.GetModeration(ctx, "ast_01old"); m.Status != "rejected" {
		t.Fatalf("decide mode: %+v", m)
	}
	// Already reviewed assets are not re-classified.
	cls.d.Status = "approved"
	e.srv.runAIModerate(ctx, job)
	if m, _ := e.store.GetModeration(ctx, "ast_01old"); m.Status != "rejected" {
		t.Fatal("re-classified a decided asset")
	}
}

func TestAIModerationNeedsAI(t *testing.T) {
	ae := newAgentEnv(t)
	expectProblem(t, ae.admin.do("PUT", "/admin/api/projects/"+ae.pid+"/moderation/settings", map[string]any{"enabled": true}, true), 422, "validation_failed")
	if r := ae.admin.do("GET", "/admin/api/ai/status", nil, false); r.json(t)["available"] != false {
		t.Fatalf("status: %s", r.body)
	}
}

func TestNextRun(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 30, 0, 0, time.UTC) // Wednesday
	cases := []struct {
		sc   db.AgentSchedule
		want string
	}{
		{db.AgentSchedule{Frequency: "daily", TimeUTC: "03:00"}, "2026-11-19T03:00"},
		{db.AgentSchedule{Frequency: "daily", TimeUTC: "11:00"}, "2026-11-18T11:00"},
		{db.AgentSchedule{Frequency: "hourly", TimeUTC: "00:15"}, "2026-11-18T11:15"},
		{db.AgentSchedule{Frequency: "hourly", TimeUTC: "00:45"}, "2026-11-18T10:45"},
		{db.AgentSchedule{Frequency: "weekly", TimeUTC: "09:00", Weekday: 1}, "2026-11-23T09:00"},
		{db.AgentSchedule{Frequency: "weekly", TimeUTC: "12:00", Weekday: 3}, "2026-11-18T12:00"},
	}
	for _, c := range cases {
		if got := nextRun(&c.sc, now).Format("2006-01-02T15:04"); got != c.want {
			t.Errorf("%+v: %s, want %s", c.sc, got, c.want)
		}
	}
}

// fakeClaude answers Messages API calls: first a tool call, then a report.
func fakeClaude(t *testing.T) (*httptest.Server, *[]map[string]any) {
	var mu sync.Mutex
	var reqs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		reqs = append(reqs, req)
		content := []map[string]any{{"type": "tool_use", "id": "tu_1", "name": "kadr_list_projects", "input": map[string]any{}}}
		stop := "tool_use"
		if len(reqs) > 1 {
			content = []map[string]any{{"type": "text", "text": "## Report\nOne project, all healthy."}}
			stop = "end_turn"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "msg", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}})
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestScheduledAgentRun(t *testing.T) {
	claude, reqs := fakeClaude(t)
	e := newEnvDev(t, nil, true, func(c *Config) {
		c.AI = ai.New("test", "", option.WithBaseURL(claude.URL), option.WithMaxRetries(0))
		c.Queue = queue.New(c.Store, nil)
	})
	ae := &agentEnv{env: e, admin: &client{e: e}}
	ae.admin.login(t, nil)
	ae.admin.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme"}, true)
	_, info := ae.issue(t, map[string]any{"name": "reporter", "level": "read"})

	expectProblem(t, ae.admin.do("POST", "/admin/api/agent-schedules", map[string]any{"name": "x", "agent_id": "agt_nope", "prompt": "p", "time_utc": "25:00"}, true), 422, "validation_failed")
	r := ae.admin.do("POST", "/admin/api/agent-schedules", map[string]any{"name": "Morning report", "agent_id": info["id"], "prompt": "Check health",
		"frequency": "daily", "time_utc": "06:00"}, true)
	if r.code != 201 || r.json(t)["next_run_at"] == nil || r.json(t)["agent_name"] != "reporter" {
		t.Fatalf("create schedule: %d %s", r.code, r.body)
	}
	sid := r.json(t)["id"].(string)
	// Make it due and tick.
	sc, _ := e.store.GetAgentSchedule(context.Background(), sid)
	past := time.Now().Add(-time.Minute)
	sc.NextRunAt = &past
	e.store.SaveAgentSchedule(context.Background(), sc)
	if err := e.srv.runAITick(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.store.ListAgentRuns(context.Background(), sid, 10)
	if len(runs) != 1 || runs[0].Status != "queued" || runs[0].Trigger != "schedule" {
		t.Fatalf("runs after tick: %+v", runs)
	}
	if sc, _ := e.store.GetAgentSchedule(context.Background(), sid); !sc.NextRunAt.After(time.Now()) {
		t.Fatalf("next run not advanced: %v", sc.NextRunAt)
	}
	job := &queue.Job{Payload: json.RawMessage(`{"run_id":"` + runs[0].ID + `"}`)}
	if err := e.srv.runAgentJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	r = ae.admin.do("GET", "/admin/api/agent-runs/"+runs[0].ID, nil, false)
	run := r.json(t)
	if run["status"] != "succeeded" || run["tool_calls"].(float64) != 1 || !strings.Contains(run["output"].(string), "all healthy") {
		t.Fatalf("run: %s", r.body)
	}
	// The tool call went through the agent's own permissions and the result reached Claude.
	second := (*reqs)[1]["messages"].([]any)
	if !strings.Contains(string(mustJSON(second[2])), "acme") {
		t.Fatalf("tool result not sent back: %v", second[2])
	}
	if tools := (*reqs)[0]["tools"].([]any); len(tools) == 0 || strings.Contains(string(mustJSON(tools)), "kadr_delete_asset") {
		t.Fatalf("tools offered to a read token: %v", tools)
	}
	if r := ae.admin.do("GET", "/admin/api/audit?actor_type=agent", nil, false); !strings.Contains(string(r.body), "agent.run") {
		t.Fatalf("audit: %s", r.body)
	}
	// Manual run of a revoked token fails cleanly.
	ae.admin.do("DELETE", "/admin/api/agents/"+info["id"].(string), nil, true)
	r = ae.admin.do("POST", "/admin/api/agent-schedules/"+sid+"/run", nil, true)
	if r.code != 202 {
		t.Fatalf("run now: %d %s", r.code, r.body)
	}
	job.Payload = json.RawMessage(`{"run_id":"` + r.json(t)["id"].(string) + `"}`)
	e.srv.runAgentJob(context.Background(), job)
	if r := ae.admin.do("GET", "/admin/api/agent-runs/"+r.json(t)["id"].(string), nil, false); r.json(t)["status"] != "failed" {
		t.Fatalf("revoked run: %s", r.body)
	}
}
