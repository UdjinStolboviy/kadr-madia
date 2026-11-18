package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/access"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// scope says how a route relates to projects, for agent tokens restricted
// to a project list.
type scope int

const (
	// scopeNone: no project data, or the handler filters by the token itself.
	scopeNone scope = iota
	// scopeProject: the {id} URL parameter is the project id.
	scopeProject
	// scopeResolve: the project is looked up from the addressed resource.
	scopeResolve
	// scopeGlobal: instance-wide; only tokens valid for all projects.
	scopeGlobal
)

// route is one Admin API endpoint with its permission annotation.
type route struct {
	method  string
	pattern string
	action  access.Action
	scope   scope
	// resolve returns the project of the addressed resource (scopeResolve).
	resolve func(ctx context.Context, st *db.Store, r *http.Request) (string, error)
	h       func(*Server) http.HandlerFunc
}

func h(f func(*Server, http.ResponseWriter, *http.Request)) func(*Server) http.HandlerFunc {
	return func(s *Server) http.HandlerFunc { return func(w http.ResponseWriter, r *http.Request) { f(s, w, r) } }
}

func resolveAPIKey(ctx context.Context, st *db.Store, r *http.Request) (string, error) {
	k, err := st.GetAPIKey(ctx, chi.URLParam(r, "key_id"))
	if err != nil {
		return "", err
	}
	return k.ProjectID, nil
}

func resolveWebhook(ctx context.Context, st *db.Store, r *http.Request) (string, error) {
	e, err := st.GetWebhookEndpoint(ctx, chi.URLParam(r, "id"))
	if err != nil {
		return "", err
	}
	return e.ProjectID, nil
}

func resolveDelivery(ctx context.Context, st *db.Store, r *http.Request) (string, error) {
	d, err := st.GetDelivery(ctx, chi.URLParam(r, "id"))
	if err != nil {
		return "", err
	}
	e, err := st.GetWebhookEndpoint(ctx, d.EndpointID)
	if err != nil {
		return "", err
	}
	return e.ProjectID, nil
}

func resolveSigningKey(ctx context.Context, st *db.Store, r *http.Request) (string, error) {
	k, err := st.GetSigningKey(ctx, chi.URLParam(r, "kid"))
	if err != nil {
		return "", err
	}
	return k.ProjectID, nil
}

func resolveCloud(ctx context.Context, st *db.Store, r *http.Request) (string, error) {
	c, err := st.GetCloudConnection(ctx, chi.URLParam(r, "id"))
	if err != nil {
		return "", err
	}
	return c.ProjectID, nil
}

// apiRoutes is the complete authenticated Admin API. Every endpoint carries
// exactly one access action (a function, not a variable: the handlers refer
// back to the router when replaying approved agent requests).
func apiRoutes() []route {
	return []route{
		// The signed-in human's own account.
		{"POST", "/auth/logout", access.SessionOnly, scopeNone, nil, h((*Server).handleLogout)},
		{"GET", "/auth/me", access.SessionOnly, scopeNone, nil, h((*Server).handleMe)},
		{"POST", "/auth/totp/setup", access.SessionOnly, scopeNone, nil, h((*Server).handleTOTPSetup)},
		{"POST", "/auth/totp/enable", access.SessionOnly, scopeNone, nil, h((*Server).handleTOTPEnable)},
		{"POST", "/auth/totp/disable", access.SessionOnly, scopeNone, nil, h((*Server).handleTOTPDisable)},
		{"POST", "/auth/webauthn/register/begin", access.SessionOnly, scopeNone, nil, h((*Server).handleWebAuthnRegisterBegin)},
		{"POST", "/auth/webauthn/register/finish", access.SessionOnly, scopeNone, nil, h((*Server).handleWebAuthnRegisterFinish)},
		{"GET", "/auth/webauthn/credentials", access.SessionOnly, scopeNone, nil, h((*Server).handleWebAuthnList)},
		{"DELETE", "/auth/webauthn/credentials/{id}", access.SessionOnly, scopeNone, nil, h((*Server).handleWebAuthnDelete)},

		// Projects, keys, presets.
		{"GET", "/projects", access.ProjectsRead, scopeNone, nil, h((*Server).handleListProjects)},
		{"POST", "/projects", access.ProjectsWrite, scopeGlobal, nil, h((*Server).handleCreateProject)},
		{"GET", "/projects/{id}", access.ProjectsRead, scopeProject, nil, h((*Server).handleGetProject)},
		{"PATCH", "/projects/{id}", access.ProjectsWrite, scopeProject, nil, h((*Server).handlePatchProject)},
		{"GET", "/projects/{id}/api-keys", access.KeysRead, scopeProject, nil, h((*Server).handleListAPIKeys)},
		{"POST", "/projects/{id}/api-keys", access.KeysIssue, scopeProject, nil, h((*Server).handleCreateAPIKey)},
		{"DELETE", "/api-keys/{key_id}", access.KeysRevoke, scopeResolve, resolveAPIKey, h((*Server).handleRevokeAPIKey)},
		{"GET", "/projects/{id}/presets", access.ProjectsRead, scopeProject, nil, h((*Server).handleListPresets)},
		{"POST", "/projects/{id}/presets/{slug}/versions", access.PresetsWrite, scopeProject, nil, h((*Server).handleSavePresetVersion)},
		{"POST", "/projects/{id}/presets/{slug}/reset", access.PresetsWrite, scopeProject, nil, h((*Server).handleResetPreset)},
		{"GET", "/projects/{id}/modes", access.ProjectsRead, scopeProject, nil, h((*Server).handleListModes)},
		{"POST", "/playground", access.PresetsWrite, scopeNone, nil, h((*Server).handlePlayground)},

		// Webhooks.
		{"GET", "/projects/{id}/webhooks", access.WebhooksRead, scopeProject, nil, h((*Server).handleListWebhooks)},
		{"POST", "/projects/{id}/webhooks", access.WebhooksWrite, scopeProject, nil, h((*Server).handleCreateWebhook)},
		{"DELETE", "/webhooks/{id}", access.WebhooksDelete, scopeResolve, resolveWebhook, h((*Server).handleDeleteWebhook)},
		{"POST", "/webhooks/{id}/test", access.WebhooksWrite, scopeResolve, resolveWebhook, h((*Server).handleTestWebhook)},
		{"GET", "/projects/{id}/deliveries", access.WebhooksRead, scopeProject, nil, h((*Server).handleListDeliveries)},
		{"POST", "/deliveries/{id}/retry", access.WebhooksWrite, scopeResolve, resolveDelivery, h((*Server).handleRetryDelivery)},

		// Storage profiles (instance-wide; credentials inside).
		{"GET", "/storage-profiles", access.StorageRead, scopeGlobal, nil, h((*Server).handleListStorageProfiles)},
		{"POST", "/storage-profiles", access.StorageManage, scopeGlobal, nil, h((*Server).handleCreateStorageProfile)},
		{"GET", "/storage-profiles/{id}", access.StorageRead, scopeGlobal, nil, h((*Server).handleGetStorageProfile)},
		{"PATCH", "/storage-profiles/{id}", access.StorageManage, scopeGlobal, nil, h((*Server).handlePatchStorageProfile)},
		{"DELETE", "/storage-profiles/{id}", access.StorageManage, scopeGlobal, nil, h((*Server).handleDeleteStorageProfile)},
		{"POST", "/storage-profiles/{id}/test", access.StorageManage, scopeGlobal, nil, h((*Server).handleTestStorageProfile)},

		// Assets and moderation.
		{"GET", "/projects/{id}/assets", access.AssetsRead, scopeProject, nil, h((*Server).handleListAssets)},
		{"GET", "/projects/{id}/assets/{asset_id}", access.AssetsRead, scopeProject, nil, h((*Server).handleGetAsset)},
		{"GET", "/projects/{id}/assets/{asset_id}/preview", access.AssetsRead, scopeProject, nil, h((*Server).handleAssetPreview)},
		{"DELETE", "/projects/{id}/assets/{asset_id}", access.AssetsDelete, scopeProject, nil, h((*Server).handleDeleteAsset)},
		{"GET", "/projects/{id}/moderation", access.AssetsRead, scopeProject, nil, h((*Server).handleModerationQueue)},
		{"GET", "/projects/{id}/assets/{asset_id}/moderation", access.AssetsRead, scopeProject, nil, h((*Server).handleGetModeration)},
		{"PUT", "/projects/{id}/assets/{asset_id}/moderation", access.ModerationWrite, scopeProject, nil, h((*Server).handleSetModeration)},

		// Analytics and billing.
		{"GET", "/projects/{id}/analytics", access.AnalyticsRead, scopeProject, nil, h((*Server).handleAnalytics)},
		{"GET", "/projects/{id}/billing", access.AnalyticsRead, scopeProject, nil, h((*Server).handleBilling)},
		{"GET", "/billing/rates", access.AnalyticsRead, scopeNone, nil, h((*Server).handleGetBillingRates)},
		{"PUT", "/billing/rates", access.BillingManage, scopeGlobal, nil, h((*Server).handlePutBillingRates)},

		// Signing keys and the edge.
		{"GET", "/projects/{id}/signing-keys", access.KeysRead, scopeProject, nil, h((*Server).handleListSigningKeys)},
		{"POST", "/projects/{id}/signing-keys", access.KeysIssue, scopeProject, nil, h((*Server).handleCreateSigningKey)},
		{"DELETE", "/signing-keys/{kid}", access.KeysRevoke, scopeResolve, resolveSigningKey, h((*Server).handleRetireSigningKey)},
		{"GET", "/signing-keys/edge-config", access.EdgePublish, scopeGlobal, nil, h((*Server).handleEdgeConfig)},
		{"POST", "/signing-keys/publish", access.EdgePublish, scopeGlobal, nil, h((*Server).handlePublishSigningKeys)},

		// Cloud drives.
		{"GET", "/projects/{id}/cloud-connections", access.KeysRead, scopeProject, nil, h((*Server).handleListCloudConnections)},
		{"POST", "/projects/{id}/cloud-connections", access.CloudConnect, scopeProject, nil, h((*Server).handleCreateCloudConnection)},
		{"DELETE", "/cloud-connections/{id}", access.KeysRevoke, scopeResolve, resolveCloud, h((*Server).handleRevokeCloudConnection)},

		// Jobs (instance-wide).
		{"GET", "/jobs", access.JobsRead, scopeGlobal, nil, h((*Server).handleListJobs)},
		{"GET", "/jobs/stream", access.JobsRead, scopeGlobal, nil, h((*Server).handleJobsStream)},
		{"POST", "/jobs/{id}/retry", access.JobsWrite, scopeGlobal, nil, h((*Server).handleRetryJob)},
		{"POST", "/jobs/{id}/cancel", access.JobsWrite, scopeGlobal, nil, h((*Server).handleCancelJob)},

		// System and audit (the audit handler filters for project-bound tokens).
		{"GET", "/system", access.SystemRead, scopeNone, nil, h((*Server).handleSystem)},
		{"GET", "/audit", access.AuditRead, scopeNone, nil, h((*Server).handleListAudit)},

		// AI agents: managed by humans only.
		{"GET", "/agents", access.AgentsView, scopeNone, nil, h((*Server).handleListAgents)},
		{"POST", "/agents", access.AgentsManage, scopeNone, nil, h((*Server).handleCreateAgent)},
		{"GET", "/agents/settings", access.AgentsView, scopeNone, nil, h((*Server).handleGetAgentSettings)},
		{"PUT", "/agents/settings", access.AgentsManage, scopeNone, nil, h((*Server).handlePutAgentSettings)},
		{"GET", "/agents/{agent_id}", access.AgentsView, scopeNone, nil, h((*Server).handleGetAgent)},
		{"PATCH", "/agents/{agent_id}", access.AgentsManage, scopeNone, nil, h((*Server).handlePatchAgent)},
		{"POST", "/agents/{agent_id}/rotate", access.AgentsManage, scopeNone, nil, h((*Server).handleRotateAgent)},
		{"DELETE", "/agents/{agent_id}", access.AgentsManage, scopeNone, nil, h((*Server).handleRevokeAgent)},
		{"GET", "/agent-approvals", access.AgentsView, scopeNone, nil, h((*Server).handleListApprovals)},
		{"GET", "/agent-approvals/{approval_id}", access.AgentsView, scopeNone, nil, h((*Server).handleGetApproval)},
		{"POST", "/agent-approvals/{approval_id}/approve", access.AgentsManage, scopeNone, nil, h((*Server).handleApprove)},
		{"POST", "/agent-approvals/{approval_id}/reject", access.AgentsManage, scopeNone, nil, h((*Server).handleReject)},

		// The calling agent itself.
		{"GET", "/agent/me", access.AgentSelf, scopeNone, nil, h((*Server).handleAgentMe)},
		{"GET", "/agent/approvals", access.AgentSelf, scopeNone, nil, h((*Server).handleAgentApprovals)},
		{"GET", "/agent/approvals/{approval_id}", access.AgentSelf, scopeNone, nil, h((*Server).handleAgentApproval)},
	}
}

func (s *Server) apiRouter() http.Handler {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	})

	r.Post("/auth/login", s.handleLogin)
	r.Post("/auth/webauthn/login/begin", s.handleWebAuthnLoginBegin)
	r.Post("/auth/webauthn/login/finish", s.handleWebAuthnLoginFinish)

	r.Group(func(r chi.Router) {
		r.Use(s.requireActor)
		r.Use(s.requireCSRF)
		for _, rt := range apiRoutes() {
			r.With(s.authorize(rt)).Method(rt.method, rt.pattern, rt.h(s))
		}
	})
	return r
}

// authorize enforces the route's action for admins (by role) and agents (by
// grant, project list, reason, rate limits and the approval policy).
func (s *Server) authorize(rt route) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a := adminFrom(r.Context()); a != nil {
				if !access.RoleAllows(a.Role, rt.action) {
					detail := "this action requires the owner or admin role"
					if a.Role == "viewer" {
						detail = "viewer role is read-only"
					}
					httpx.WriteProblem(w, http.StatusForbidden, "forbidden", detail)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			t := agentFrom(r.Context())
			if t == nil {
				httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "login required")
				return
			}
			s.authorizeAgent(w, r, t, rt, next)
		})
	}
}

func (s *Server) authorizeAgent(w http.ResponseWriter, r *http.Request, t *db.AgentToken, rt route, next http.Handler) {
	ctx := r.Context()
	replay := approvedFrom(ctx) != ""
	unsafe := isUnsafe(r.Method)
	if !replay && !s.agentLimit.Allow(t.ID) {
		w.Header().Set("Retry-After", "10")
		httpx.WriteProblem(w, http.StatusTooManyRequests, "rate_limited", "too many requests from this agent token")
		return
	}
	if !grantOf(t).Allows(rt.action) {
		s.auditAgentDenied(r, t, rt, "", "insufficient_scope")
		httpx.WriteProblem(w, http.StatusForbidden, "insufficient_scope",
			"this agent token does not grant "+string(rt.action)+" (ask an administrator to raise its level or grant the permission)")
		return
	}
	var projectID string
	switch rt.scope {
	case scopeGlobal:
		if !t.AllProjects() {
			s.auditAgentDenied(r, t, rt, "", "project_scope")
			httpx.WriteProblem(w, http.StatusForbidden, "project_not_allowed", "this endpoint is instance-wide; it needs a token valid for all projects")
			return
		}
	case scopeProject:
		projectID = chi.URLParam(r, "id")
	case scopeResolve:
		id, err := rt.resolve(ctx, s.store, r)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			s.internalError(w, r, err)
			return
		}
		projectID = id // not found: the handler answers 404
	}
	if projectID != "" && !t.CoversProject(projectID) {
		s.auditAgentDenied(r, t, rt, projectID, "project_scope")
		httpx.WriteProblem(w, http.StatusForbidden, "project_not_allowed", "this agent token is not valid for project "+projectID)
		return
	}
	if unsafe {
		if n := len([]rune(agentReason(r))); n < minReasonLen || n > maxReasonLen {
			httpx.WriteProblem(w, http.StatusUnprocessableEntity, "reason_required",
				"state-changing agent requests must explain themselves in the "+HeaderAgentReason+" header (3–500 characters)")
			return
		}
		if !replay && !s.agentWriteLimit.Allow(t.ID) {
			w.Header().Set("Retry-After", "30")
			httpx.WriteProblem(w, http.StatusTooManyRequests, "rate_limited", "too many changes from this agent token; slow down")
			return
		}
	}
	if !replay && rt.pattern != "/playground" && needsApproval(t, rt.action, r.Method) { // the playground has no side effects
		s.holdForApproval(w, r, t, rt, projectID)
		return
	}
	if r.Method == http.MethodDelete && !s.agentDeletes.allow(t.ID, t.MaxDeletesPerHour, s.now()) {
		s.auditAgentDenied(r, t, rt, projectID, "delete_budget")
		w.Header().Set("Retry-After", "600")
		httpx.WriteProblem(w, http.StatusTooManyRequests, "delete_budget_exhausted",
			"this agent token reached its hourly deletion budget; an administrator can raise it")
		return
	}
	next.ServeHTTP(w, r)
}

// needsApproval applies the token's approval mode.
func needsApproval(t *db.AgentToken, a access.Action, method string) bool {
	switch t.ApprovalMode {
	case "writes":
		return isUnsafe(method) || access.IsDestructive(a)
	case "destructive":
		return access.IsDestructive(a)
	}
	return false
}

// auditAgentDenied records a refused agent request (security signal).
func (s *Server) auditAgentDenied(r *http.Request, t *db.AgentToken, rt route, projectID, why string) {
	var pid *string
	if projectID != "" {
		pid = &projectID
	}
	target := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/admin/api")
	details := withReason(r, map[string]any{"required": string(rt.action), "why": why})
	if err := s.store.InsertAudit(r.Context(), &db.AuditEntry{ActorType: db.ActorAgent, ActorID: &t.ID, ProjectID: pid,
		Action: "agent.denied", Target: &target, IP: ptrTo(httpx.ClientIP(r)), CreatedAt: s.now()}, details); err != nil {
		s.log.WarnContext(r.Context(), "audit agent denial failed", "err", err)
	}
}

func ptrTo[T any](v T) *T { return &v }
