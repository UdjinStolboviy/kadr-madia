package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/access"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// agentClient calls the admin listener with an agent token.
type agentClient struct {
	e     *env
	token string
	ip    string
}

func (a *agentClient) do(method, path string, body any, reason string) resp {
	a.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	if reason != "" {
		req.Header.Set(HeaderAgentReason, reason)
	}
	if a.ip != "" {
		req.RemoteAddr = a.ip + ":1234"
	}
	rec := httptest.NewRecorder()
	a.e.admin.ServeHTTP(rec, req)
	return resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.Bytes()}
}

// rpc sends one MCP JSON-RPC request to /mcp.
func (a *agentClient) rpc(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	r := a.rpcRaw(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, nil)
	if r.code != 200 {
		t.Fatalf("%s: %d %s", method, r.code, r.body)
	}
	return r.json(t)
}

func (a *agentClient) rpcRaw(msg any, hdr map[string]string) resp {
	b, _ := json.Marshal(msg)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.e.admin.ServeHTTP(rec, req)
	return resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.Bytes()}
}

func (a *agentClient) callTool(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	m := a.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})
	if m["error"] != nil {
		t.Fatalf("tools/call %s: %v", name, m["error"])
	}
	return m["result"].(map[string]any)
}

func toolText(res map[string]any) string {
	var sb strings.Builder
	for _, c := range res["content"].([]any) {
		if m := c.(map[string]any); m["type"] == "text" {
			sb.WriteString(m["text"].(string))
		}
	}
	return sb.String()
}

type agentEnv struct {
	*env
	admin *client
	pid   string // project "acme"
	pid2  string // project "beta"
}

func newAgentEnv(t *testing.T) *agentEnv {
	e := newEnv(t, nil)
	c := &client{e: e}
	if r := c.login(t, nil); r.code != 200 {
		t.Fatalf("login: %d %s", r.code, r.body)
	}
	ae := &agentEnv{env: e, admin: c}
	for i, slug := range []string{"acme", "beta"} {
		r := c.do("POST", "/admin/api/projects", map[string]any{"slug": slug, "name": slug}, true)
		if r.code != 201 {
			t.Fatalf("create project: %d %s", r.code, r.body)
		}
		if i == 0 {
			ae.pid = r.json(t)["id"].(string)
		} else {
			ae.pid2 = r.json(t)["id"].(string)
		}
	}
	return ae
}

// issue creates an agent token through the admin API.
func (ae *agentEnv) issue(t *testing.T, body map[string]any) (*agentClient, map[string]any) {
	t.Helper()
	r := ae.admin.do("POST", "/admin/api/agents", body, true)
	if r.code != 201 {
		t.Fatalf("create agent: %d %s", r.code, r.body)
	}
	m := r.json(t)
	return &agentClient{e: ae.env, token: m["token"].(string)}, m["agent"].(map[string]any)
}

func TestEveryRouteIsAnnotated(t *testing.T) {
	seen := map[string]bool{}
	for _, rt := range apiRoutes() {
		key := rt.method + " " + rt.pattern
		if seen[key] {
			t.Errorf("duplicate route %s", key)
		}
		seen[key] = true
		if rt.action == "" || rt.h == nil {
			t.Errorf("%s: missing action or handler", key)
		}
		if rt.scope == scopeResolve && rt.resolve == nil {
			t.Errorf("%s: scopeResolve without resolver", key)
		}
		if rt.scope == scopeProject && !strings.HasPrefix(rt.pattern, "/projects/{id}") {
			t.Errorf("%s: scopeProject but no {id} project parameter", key)
		}
		if strings.HasPrefix(rt.pattern, "/auth/") && rt.action != access.SessionOnly {
			t.Errorf("%s: own-account routes must be session-only", key)
		}
		if strings.HasPrefix(rt.pattern, "/agents") || strings.HasPrefix(rt.pattern, "/agent-approvals") {
			if rt.action != access.AgentsView && rt.action != access.AgentsManage {
				t.Errorf("%s: agent management must never be reachable by agents", key)
			}
		}
		if isUnsafe(rt.method) && access.IsRead(rt.action) {
			t.Errorf("%s: state-changing route annotated with read action %s", key, rt.action)
		}
	}
	// Every MCP tool maps to a route, and its kind matches the route's method.
	routes := apiRoutes()
	for _, spec := range toolSpecs() {
		rt, ok := specRoute(spec, routes)
		if !ok {
			t.Errorf("tool %s: no route %s %s", spec.name, spec.method, spec.path)
			continue
		}
		if spec.custom == nil && (spec.kind == kindRead) == isUnsafe(rt.method) {
			t.Errorf("tool %s: kind does not match method %s", spec.name, rt.method)
		}
		if rt.method == "DELETE" && spec.kind != kindDestructive {
			t.Errorf("tool %s: DELETE must be marked destructive", spec.name)
		}
	}
}

func TestAgentReadTokenPermissions(t *testing.T) {
	ae := newAgentEnv(t)
	ag, info := ae.issue(t, map[string]any{"name": "reporter", "level": "read"})
	if !strings.HasPrefix(ag.token, "kd_agent_") || info["status"] != "active" || info["approval_mode"] != "destructive" ||
		info["project_ids"] != nil || info["expires_at"] == nil {
		t.Fatalf("issued: %v", info)
	}
	// The secret is never listed.
	if r := ae.admin.do("GET", "/admin/api/agents", nil, false); strings.Contains(string(r.body), ag.token) {
		t.Fatal("agent list leaks the secret")
	}

	if r := ag.do("GET", "/admin/api/projects", nil, ""); r.code != 200 || len(r.json(t)["items"].([]any)) != 2 {
		t.Fatalf("agent list projects: %d %s", r.code, r.body)
	}
	if r := ag.do("GET", "/admin/api/agent/me", nil, ""); r.code != 200 || r.json(t)["level"] != "read" {
		t.Fatalf("agent me: %d %s", r.code, r.body)
	}
	expectProblem(t, ag.do("PATCH", "/admin/api/projects/"+ae.pid, map[string]any{"name": "x"}, "rename"), 403, "insufficient_scope")
	expectProblem(t, ag.do("GET", "/admin/api/auth/me", nil, ""), 403, "insufficient_scope")
	expectProblem(t, ag.do("GET", "/admin/api/agents", nil, ""), 403, "insufficient_scope")
	expectProblem(t, ag.do("POST", "/admin/api/agents", map[string]any{"name": "evil", "level": "full"}, "escalate"), 403, "insufficient_scope")
	// No CSRF needed for bearer tokens, but no cookie session either.
	expectProblem(t, (&agentClient{e: ae.env, token: "kd_agent_AAAAAAAA_" + strings.Repeat("A", 32)}).do("GET", "/admin/api/projects", nil, ""), 401, "unauthenticated")

	// Denials are audited.
	r := ae.admin.do("GET", "/admin/api/audit?actor_type=agent", nil, false)
	if !strings.Contains(string(r.body), "agent.denied") {
		t.Fatalf("denial not audited: %s", r.body)
	}
}

func TestAgentEditTokenNeedsReasonAndIsAudited(t *testing.T) {
	ae := newAgentEnv(t)
	ag, info := ae.issue(t, map[string]any{"name": "ops", "level": "edit", "approval_mode": "none"})
	expectProblem(t, ag.do("PATCH", "/admin/api/projects/"+ae.pid, map[string]any{"name": "Acme 2"}, ""), 422, "reason_required")
	if r := ag.do("PATCH", "/admin/api/projects/"+ae.pid, map[string]any{"name": "Acme 2"}, "align the display name with the brand"); r.code != 200 {
		t.Fatalf("patch: %d %s", r.code, r.body)
	}
	// Edit cannot delete.
	expectProblem(t, ag.do("DELETE", "/admin/api/projects/"+ae.pid+"/assets/ast_x", nil, "cleanup"), 403, "insufficient_scope")

	r := ae.admin.do("GET", "/admin/api/audit?actor_type=agent&actor_id="+info["id"].(string), nil, false)
	var page struct {
		Items []struct {
			Action  string         `json:"action"`
			Details map[string]any `json:"details"`
		} `json:"items"`
	}
	json.Unmarshal(r.body, &page)
	found := false
	for _, it := range page.Items {
		if it.Action == "project.update" && it.Details["reason"] == "align the display name with the brand" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit lacks the agent change with its reason: %s", r.body)
	}
}

func TestAgentProjectScope(t *testing.T) {
	ae := newAgentEnv(t)
	ag, _ := ae.issue(t, map[string]any{"name": "acme-only", "level": "edit", "project_ids": []string{ae.pid}, "approval_mode": "none"})
	r := ag.do("GET", "/admin/api/projects", nil, "")
	if items := r.json(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != ae.pid {
		t.Fatalf("scoped list: %s", r.body)
	}
	if r := ag.do("GET", "/admin/api/projects/"+ae.pid, nil, ""); r.code != 200 {
		t.Fatalf("own project: %d", r.code)
	}
	expectProblem(t, ag.do("GET", "/admin/api/projects/"+ae.pid2, nil, ""), 403, "project_not_allowed")
	expectProblem(t, ag.do("GET", "/admin/api/jobs", nil, ""), 403, "project_not_allowed")
	expectProblem(t, ag.do("POST", "/admin/api/projects", map[string]any{"slug": "gamma", "name": "G"}, "new tenant"), 403, "project_not_allowed")

	// Resources of other projects are resolved and refused too.
	r = ae.admin.do("POST", "/admin/api/projects/"+ae.pid2+"/webhooks", map[string]any{"url": "https://hooks.example.com/b", "events": []string{"*"}}, true)
	hid := r.json(t)["webhook"].(map[string]any)["id"].(string)
	expectProblem(t, ag.do("POST", "/admin/api/webhooks/"+hid+"/test", nil, "check"), 403, "project_not_allowed")

	// The audit log is limited to the token's projects.
	r = ag.do("GET", "/admin/api/audit", nil, "")
	if r.code != 200 || strings.Contains(string(r.body), ae.pid2) {
		t.Fatalf("scoped audit: %d %s", r.code, r.body)
	}
	// Unknown project ids are rejected at issue time.
	r = ae.admin.do("POST", "/admin/api/agents", map[string]any{"name": "x", "level": "read", "project_ids": []string{"prj_nope"}}, true)
	expectProblem(t, r, 422, "validation_failed")
}

func TestAgentApprovalFlow(t *testing.T) {
	ae := newAgentEnv(t)
	ag, info := ae.issue(t, map[string]any{"name": "janitor", "level": "full"}) // approval_mode defaults to destructive
	r := ag.do("POST", "/admin/api/projects/"+ae.pid+"/webhooks", map[string]any{"url": "https://hooks.example.com/a", "events": []string{"*"}}, "subscribe to asset events")
	if r.code != 201 {
		t.Fatalf("create webhook runs immediately: %d %s", r.code, r.body)
	}
	hid := r.json(t)["webhook"].(map[string]any)["id"].(string)

	r = ag.do("DELETE", "/admin/api/webhooks/"+hid, nil, "endpoint is decommissioned")
	if r.code != 202 || r.json(t)["status"] != "pending_approval" {
		t.Fatalf("delete should be held: %d %s", r.code, r.body)
	}
	aid := r.json(t)["approval_id"].(string)
	// Retrying does not create a second approval.
	if r2 := ag.do("DELETE", "/admin/api/webhooks/"+hid, nil, "endpoint is decommissioned"); r2.json(t)["approval_id"] != aid {
		t.Fatalf("duplicate approval: %s", r2.body)
	}
	// Still there.
	if r := ae.admin.do("GET", "/admin/api/projects/"+ae.pid+"/webhooks", nil, false); len(r.json(t)["items"].([]any)) != 1 {
		t.Fatalf("webhook deleted before approval: %s", r.body)
	}
	r = ae.admin.do("GET", "/admin/api/agent-approvals?status=pending", nil, false)
	if items := r.json(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["agent_name"] != "janitor" ||
		items[0].(map[string]any)["reason"] != "endpoint is decommissioned" {
		t.Fatalf("pending list: %s", r.body)
	}
	if r := ae.admin.do("GET", "/admin/api/agents/settings", nil, false); r.json(t)["pending_approvals"].(float64) != 1 {
		t.Fatalf("settings pending count: %s", r.body)
	}
	r = ae.admin.do("POST", "/admin/api/agent-approvals/"+aid+"/approve", map[string]any{"note": "ok"}, true)
	if r.code != 200 || r.json(t)["status"] != "executed" || r.json(t)["result_status"].(float64) != 204 {
		t.Fatalf("approve: %d %s", r.code, r.body)
	}
	if r := ae.admin.do("GET", "/admin/api/projects/"+ae.pid+"/webhooks", nil, false); len(r.json(t)["items"].([]any)) != 0 {
		t.Fatalf("webhook not deleted after approval: %s", r.body)
	}
	expectProblem(t, ae.admin.do("POST", "/admin/api/agent-approvals/"+aid+"/approve", nil, true), 409, "not_pending")
	if r := ag.do("GET", "/admin/api/agent/approvals/"+aid, nil, ""); r.code != 200 || r.json(t)["status"] != "executed" {
		t.Fatalf("agent approval view: %d %s", r.code, r.body)
	}
	// Agents cannot decide approvals.
	expectProblem(t, ag.do("POST", "/admin/api/agent-approvals/"+aid+"/approve", nil, "self approve"), 403, "insufficient_scope")

	// Sensitive results are visible to the agent but masked for admins; a
	// rejected request never runs.
	r = ag.do("DELETE", "/admin/api/projects/"+ae.pid+"/assets/ast_missing", nil, "remove spam")
	aid2 := r.json(t)["approval_id"].(string)
	r = ae.admin.do("POST", "/admin/api/agent-approvals/"+aid2+"/reject", map[string]any{"note": "keep it"}, true)
	if r.code != 200 || r.json(t)["status"] != "rejected" {
		t.Fatalf("reject: %d %s", r.code, r.body)
	}
	if r := ag.do("GET", "/admin/api/agent/approvals/"+aid2, nil, ""); r.json(t)["decision_note"] != "keep it" {
		t.Fatalf("agent sees rejection note: %s", r.body)
	}

	// Revoking the token rejects what is still pending and locks it out.
	r = ag.do("DELETE", "/admin/api/projects/"+ae.pid+"/assets/ast_other", nil, "remove spam")
	aid3 := r.json(t)["approval_id"].(string)
	if r := ae.admin.do("DELETE", "/admin/api/agents/"+info["id"].(string), nil, true); r.code != 204 {
		t.Fatalf("revoke: %d", r.code)
	}
	expectProblem(t, ag.do("GET", "/admin/api/projects", nil, ""), 401, "unauthenticated")
	if r := ae.admin.do("GET", "/admin/api/agent-approvals/"+aid3, nil, false); r.json(t)["status"] != "rejected" {
		t.Fatalf("pending approval after revoke: %s", r.body)
	}
}

func TestAgentApprovalModeWrites(t *testing.T) {
	ae := newAgentEnv(t)
	ag, _ := ae.issue(t, map[string]any{"name": "careful", "level": "edit", "approval_mode": "writes"})
	if r := ag.do("GET", "/admin/api/projects", nil, ""); r.code != 200 {
		t.Fatalf("reads run: %d", r.code)
	}
	r := ag.do("PATCH", "/admin/api/projects/"+ae.pid, map[string]any{"name": "Renamed"}, "rename")
	if r.code != 202 {
		t.Fatalf("write should be held: %d %s", r.code, r.body)
	}
	aid := r.json(t)["approval_id"].(string)
	// The admin sees the request body.
	r = ae.admin.do("GET", "/admin/api/agent-approvals/"+aid, nil, false)
	if r.json(t)["body"].(map[string]any)["name"] != "Renamed" {
		t.Fatalf("approval body: %s", r.body)
	}
	if r := ae.admin.do("POST", "/admin/api/agent-approvals/"+aid+"/approve", nil, true); r.json(t)["result_status"].(float64) != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	if r := ae.admin.do("GET", "/admin/api/projects/"+ae.pid, nil, false); r.json(t)["name"] != "Renamed" {
		t.Fatalf("not applied: %s", r.body)
	}
}

func TestAgentKillSwitchIPAndDeleteBudget(t *testing.T) {
	ae := newAgentEnv(t)
	ag, info := ae.issue(t, map[string]any{"name": "cleaner", "level": "full", "approval_mode": "none", "max_deletes_per_hour": 1,
		"allowed_ips": []string{"192.0.2.0/24"}})
	ag.ip = "192.0.2.10"
	for _, u := range []string{"https://hooks.example.com/1", "https://hooks.example.com/2"} {
		if r := ag.do("POST", "/admin/api/projects/"+ae.pid+"/webhooks", map[string]any{"url": u, "events": []string{"*"}}, "setup"); r.code != 201 {
			t.Fatalf("create: %d %s", r.code, r.body)
		}
	}
	r := ae.admin.do("GET", "/admin/api/projects/"+ae.pid+"/webhooks", nil, false)
	items := r.json(t)["items"].([]any)
	if r := ag.do("DELETE", "/admin/api/webhooks/"+items[0].(map[string]any)["id"].(string), nil, "cleanup"); r.code != 204 {
		t.Fatalf("first delete: %d %s", r.code, r.body)
	}
	expectProblem(t, ag.do("DELETE", "/admin/api/webhooks/"+items[1].(map[string]any)["id"].(string), nil, "cleanup"), 429, "delete_budget_exhausted")

	// IP allowlist.
	other := &agentClient{e: ae.env, token: ag.token, ip: "198.51.100.7"}
	expectProblem(t, other.do("GET", "/admin/api/projects", nil, ""), 403, "ip_not_allowed")

	// Global switch.
	if r := ae.admin.do("PUT", "/admin/api/agents/settings", map[string]any{"enabled": false}, true); r.code != 200 || r.json(t)["enabled"] != false {
		t.Fatalf("disable: %d %s", r.code, r.body)
	}
	expectProblem(t, ag.do("GET", "/admin/api/projects", nil, ""), 403, "agents_disabled")
	ae.admin.do("PUT", "/admin/api/agents/settings", map[string]any{"enabled": true}, true)
	if r := ag.do("GET", "/admin/api/projects", nil, ""); r.code != 200 {
		t.Fatalf("re-enabled: %d", r.code)
	}

	// Rotation invalidates the old secret.
	r = ae.admin.do("POST", "/admin/api/agents/"+info["id"].(string)+"/rotate", nil, true)
	if r.code != 200 {
		t.Fatalf("rotate: %d %s", r.code, r.body)
	}
	expectProblem(t, ag.do("GET", "/admin/api/projects", nil, ""), 401, "unauthenticated")
	ag.token = r.json(t)["token"].(string)
	if r := ag.do("GET", "/admin/api/projects", nil, ""); r.code != 200 {
		t.Fatalf("new secret: %d", r.code)
	}

	// Expired tokens stop working.
	id := info["id"].(string)
	tk, _ := ae.store.GetAgentToken(context.Background(), id)
	past := time.Now().Add(-time.Minute)
	tk.ExpiresAt = &past
	ae.store.UpdateAgentToken(context.Background(), tk)
	expectProblem(t, ag.do("GET", "/admin/api/projects", nil, ""), 401, "unauthenticated")
}

func TestAgentIssuingRules(t *testing.T) {
	ae := newAgentEnv(t)
	// Sensitive scopes need edit+.
	expectProblem(t, ae.admin.do("POST", "/admin/api/agents", map[string]any{"name": "x", "level": "read", "extra_scopes": []string{"keys:issue"}}, true), 422, "validation_failed")
	expectProblem(t, ae.admin.do("POST", "/admin/api/agents", map[string]any{"name": "x", "level": "edit", "extra_scopes": []string{"root"}}, true), 422, "validation_failed")
	expectProblem(t, ae.admin.do("POST", "/admin/api/agents", map[string]any{"name": "", "level": "god"}, true), 422, "validation_failed")
	ag, info := ae.issue(t, map[string]any{"name": "keys", "level": "edit", "extra_scopes": []string{"keys:issue"}, "approval_mode": "none", "expires_in_days": 0})
	if info["expires_at"] != nil {
		t.Fatalf("never-expiring token: %v", info)
	}
	if r := ag.do("POST", "/admin/api/projects/"+ae.pid+"/api-keys", map[string]any{"name": "backend", "scopes": []string{"assets:read"}}, "backend integration"); r.code != 201 {
		t.Fatalf("granted keys:issue: %d %s", r.code, r.body)
	}
	// Edit without the grant cannot issue keys.
	ag2, _ := ae.issue(t, map[string]any{"name": "nokeys", "level": "full", "approval_mode": "none"})
	expectProblem(t, ag2.do("POST", "/admin/api/projects/"+ae.pid+"/api-keys", map[string]any{"name": "b", "scopes": []string{"assets:read"}}, "x integration"), 403, "insufficient_scope")

	// Non-owner admins cannot grant sensitive permissions; viewers cannot manage agents.
	for _, role := range []string{"admin", "viewer"} {
		hash, _ := auth.HashPassword(testPassword)
		email := role + "-2@example.com"
		ae.store.CreateAdmin(context.Background(), &db.Admin{ID: db.NewID(db.PrefixAdmin), Email: email, PasswordHash: hash, Role: role, CreatedAt: time.Now()})
		c := &client{e: ae.env}
		r := c.do("POST", "/admin/api/auth/login", map[string]any{"email": email, "password": testPassword}, false)
		c.csrf = r.json(t)["csrf_token"].(string)
		r = c.do("POST", "/admin/api/agents", map[string]any{"name": "y", "level": "edit", "extra_scopes": []string{"storage:manage"}}, true)
		if role == "admin" {
			expectProblem(t, r, 422, "validation_failed")
			if r := c.do("POST", "/admin/api/agents", map[string]any{"name": "y", "level": "full"}, true); r.code != 201 {
				t.Fatalf("admin issues full: %d %s", r.code, r.body)
			}
		} else {
			expectProblem(t, r, 403, "forbidden")
			if r := c.do("GET", "/admin/api/agents", nil, false); r.code != 200 {
				t.Fatalf("viewer can list agents: %d", r.code)
			}
		}
	}
}

func TestMCPProtocol(t *testing.T) {
	ae := newAgentEnv(t)
	reader, _ := ae.issue(t, map[string]any{"name": "reader", "level": "read", "project_ids": []string{ae.pid}})

	// Transport rules.
	if r := (&agentClient{e: ae.env}).rpcRaw(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, nil); r.code != 401 ||
		!strings.Contains(r.hdr.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("unauthenticated: %d %v", r.code, r.hdr)
	}
	req := httptest.NewRequest("GET", "/mcp", nil)
	rec := httptest.NewRecorder()
	ae.env.admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp: %d", rec.Code)
	}
	if r := reader.rpcRaw(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil); r.code != 202 {
		t.Fatalf("notification: %d", r.code)
	}
	if r := reader.rpcRaw(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, map[string]string{"Origin": "https://evil.example"}); r.code != 403 {
		t.Fatalf("foreign origin: %d", r.code)
	}

	init := reader.rpc(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}})["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || !strings.Contains(init["instructions"].(string), "reader") {
		t.Fatalf("initialize: %v", init)
	}
	tools := reader.rpc(t, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, x := range tools {
		tl := x.(map[string]any)
		names[tl["name"].(string)] = true
		if tl["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Errorf("read token sees write tool %s", tl["name"])
		}
	}
	if !names["kadr_list_projects"] || !names["kadr_view_asset"] || names["kadr_delete_asset"] || names["kadr_list_jobs"] || names["kadr_update_project"] {
		t.Fatalf("tools for scoped read token: %v", names)
	}
	res := reader.callTool(t, "kadr_list_projects", nil)
	if res["isError"] == true || !strings.Contains(toolText(res), ae.pid) || strings.Contains(toolText(res), ae.pid2) {
		t.Fatalf("list projects: %v", res)
	}
	// Hidden tools cannot be called.
	if m := reader.rpc(t, "tools/call", map[string]any{"name": "kadr_delete_asset", "arguments": map[string]any{}}); m["error"] == nil {
		t.Fatalf("hidden tool callable: %v", m)
	}
	// Tool errors come back as isError results.
	if res := reader.callTool(t, "kadr_get_project", map[string]any{"project_id": ae.pid2}); res["isError"] != true ||
		!strings.Contains(toolText(res), "project_not_allowed") {
		t.Fatalf("other project: %v", res)
	}
	if res := reader.callTool(t, "kadr_get_project", map[string]any{}); res["isError"] != true {
		t.Fatalf("missing argument: %v", res)
	}
	if rs := reader.rpc(t, "resources/read", map[string]any{"uri": "kadr://guide/agents"})["result"].(map[string]any); !strings.Contains(string(mustJSON(rs)), "Workflows") {
		t.Fatalf("guide resource: %v", rs)
	}
	pr := reader.rpc(t, "prompts/get", map[string]any{"name": "moderate_assets", "arguments": map[string]any{"project_id": ae.pid}})
	if pr["error"] != nil || !strings.Contains(string(mustJSON(pr)), "kadr_view_asset") {
		t.Fatalf("prompt: %v", pr)
	}
	if m := reader.rpc(t, "nope/method", nil); m["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", m)
	}

	// An edit token changes things through tools, with a reason.
	editor, _ := ae.issue(t, map[string]any{"name": "editor", "level": "edit", "approval_mode": "none"})
	if res := editor.callTool(t, "kadr_update_project", map[string]any{"project_id": ae.pid, "name": "Via MCP"}); res["isError"] != true {
		t.Fatalf("missing reason accepted: %v", res)
	}
	res = editor.callTool(t, "kadr_update_project", map[string]any{"project_id": ae.pid, "name": "Via MCP", "reason": "rename requested by the user"})
	if res["isError"] == true {
		t.Fatalf("update via MCP: %v", res)
	}
	if r := ae.admin.do("GET", "/admin/api/projects/"+ae.pid, nil, false); r.json(t)["name"] != "Via MCP" {
		t.Fatalf("not applied: %s", r.body)
	}
	r := ae.admin.do("GET", "/admin/api/audit?actor_type=agent", nil, false)
	if !strings.Contains(string(r.body), `"tool":"kadr_update_project"`) {
		t.Fatalf("audit lacks tool name: %s", r.body)
	}

	// Batches are answered as arrays.
	b := reader.rpcRaw([]any{map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"}}, nil)
	var arr []any
	if json.Unmarshal(b.body, &arr); len(arr) != 2 {
		t.Fatalf("batch: %s", b.body)
	}
}

func TestModeration(t *testing.T) {
	ae := newAgentEnv(t)
	ctx := context.Background()
	w := ae.store.DB().W
	for _, id := range []string{"ast_01", "ast_02"} {
		if _, err := w.ExecContext(ctx, `INSERT INTO assets (id, project_id, ns, kind, visibility, status, created_at) VALUES (?, ?, 'u1', 'image', 'public', 'ready', 1)`, id, ae.pid); err != nil {
			t.Fatal(err)
		}
	}
	ae.admin.do("POST", "/admin/api/projects/"+ae.pid+"/webhooks", map[string]any{"url": "https://hooks.example.com/m", "events": []string{"asset.moderated"}}, true)
	ag, _ := ae.issue(t, map[string]any{"name": "moderator", "level": "edit", "approval_mode": "none"})

	r := ag.do("GET", "/admin/api/projects/"+ae.pid+"/moderation", nil, "")
	if r.code != 200 || len(r.json(t)["items"].([]any)) != 2 {
		t.Fatalf("queue: %d %s", r.code, r.body)
	}
	expectProblem(t, ag.do("PUT", "/admin/api/projects/"+ae.pid+"/assets/ast_01/moderation", map[string]any{"status": "maybe"}, "review"), 422, "validation_failed")
	r = ag.do("PUT", "/admin/api/projects/"+ae.pid+"/assets/ast_01/moderation", map[string]any{"status": "flagged", "labels": []string{"NSFW", "nsfw"}, "note": "partial nudity"}, "policy review")
	if r.code != 200 || r.json(t)["status"] != "flagged" || len(r.json(t)["labels"].([]any)) != 1 || r.json(t)["actor_type"] != "agent" {
		t.Fatalf("moderate: %d %s", r.code, r.body)
	}
	r = ag.do("GET", "/admin/api/projects/"+ae.pid+"/moderation", nil, "")
	if items := r.json(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["asset_id"] != "ast_02" {
		t.Fatalf("unreviewed after decision: %s", r.body)
	}
	r = ag.do("GET", "/admin/api/projects/"+ae.pid+"/moderation?status=flagged", nil, "")
	if items := r.json(t)["items"].([]any); len(items) != 1 {
		t.Fatalf("flagged list: %s", r.body)
	}
	var n int
	ae.store.DB().R.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE type = 'asset.moderated'`).Scan(&n)
	if n != 1 {
		t.Fatalf("asset.moderated events = %d", n)
	}
	expectProblem(t, ag.do("PUT", "/admin/api/projects/"+ae.pid+"/assets/ast_zz/moderation", map[string]any{"status": "approved"}, "review"), 404, "not_found")
	// Read-only agents cannot moderate.
	ro, _ := ae.issue(t, map[string]any{"name": "ro", "level": "read"})
	expectProblem(t, ro.do("PUT", "/admin/api/projects/"+ae.pid+"/assets/ast_02/moderation", map[string]any{"status": "approved"}, "review"), 403, "insufficient_scope")
}
