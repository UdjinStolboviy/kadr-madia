package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/access"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

const (
	maxAgentNameLen      = 100
	maxAgentDescLen      = 500
	maxAgentProjects     = 100
	maxAgentIPs          = 20
	maxDeletesPerHourCap = 10000
	defaultAgentDays     = 90
	maxAgentDays         = 365
	defaultDeletesHour   = 50
)

// ───────────── DTOs ─────────────

type agentDTO struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Prefix            string   `json:"prefix"`
	Level             string   `json:"level"`
	ExtraScopes       []string `json:"extra_scopes"`
	Permissions       []string `json:"permissions"`
	ProjectIDs        []string `json:"project_ids"` // null = all projects
	ApprovalMode      string   `json:"approval_mode"`
	AllowedIPs        []string `json:"allowed_ips"`
	MaxDeletesPerHour int      `json:"max_deletes_per_hour"`
	Status            string   `json:"status"` // active | expired | revoked
	CreatedBy         string   `json:"created_by"`
	OAuthClientID     *string  `json:"oauth_client_id"` // connected through OAuth (e.g. a claude.ai connector)
	LastUsedAt        *string  `json:"last_used_at"`
	LastUsedIP        *string  `json:"last_used_ip"`
	ExpiresAt         *string  `json:"expires_at"`
	RevokedAt         *string  `json:"revoked_at"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

func agentStatus(t *db.AgentToken, now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case t.ExpiresAt != nil && !now.Before(*t.ExpiresAt):
		return "expired"
	}
	return "active"
}

func (s *Server) toAgentDTO(t *db.AgentToken) agentDTO {
	return agentDTO{ID: t.ID, Name: t.Name, Description: t.Description, Prefix: t.Prefix, Level: t.Level,
		ExtraScopes: t.ExtraScopes, Permissions: grantOf(t).Actions(), ProjectIDs: t.ProjectIDs, ApprovalMode: t.ApprovalMode,
		AllowedIPs: t.AllowedIPs, MaxDeletesPerHour: t.MaxDeletesPerHour, Status: agentStatus(t, s.now()), CreatedBy: t.CreatedBy, OAuthClientID: t.OAuthClientID,
		LastUsedAt: tsp(t.LastUsedAt), LastUsedIP: t.LastUsedIP, ExpiresAt: tsp(t.ExpiresAt), RevokedAt: tsp(t.RevokedAt),
		CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt)}
}

// mcpSetup is shown once after issuing or rotating a token.
type mcpSetup struct {
	URL               string `json:"url"`
	ClaudeCodeCommand string `json:"claude_code_command"`
	JSONConfig        string `json:"json_config"`
}

func (s *Server) mcpURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/") + "/mcp"
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp"
}

func (s *Server) mcpSetupFor(r *http.Request, secret string) mcpSetup {
	u := s.mcpURL(r)
	return mcpSetup{
		URL:               u,
		ClaudeCodeCommand: fmt.Sprintf(`claude mcp add --transport http kadr %s --header "Authorization: Bearer %s"`, u, secret),
		JSONConfig: fmt.Sprintf(`{
  "mcpServers": {
    "kadr": {
      "type": "http",
      "url": %q,
      "headers": { "Authorization": "Bearer %s" }
    }
  }
}`, u, secret),
	}
}

// ───────────── requests ─────────────

type agentInput struct {
	Name              *string   `json:"name"`
	Description       *string   `json:"description"`
	Level             *string   `json:"level"`
	ExtraScopes       *[]string `json:"extra_scopes"`
	ProjectIDs        *[]string `json:"project_ids"` // explicit null = all projects
	ApprovalMode      *string   `json:"approval_mode"`
	AllowedIPs        *[]string `json:"allowed_ips"`
	MaxDeletesPerHour *int      `json:"max_deletes_per_hour"`
	ExpiresInDays     *int      `json:"expires_in_days"` // 0 = never
	// allProjects is set when "project_ids" is present and null.
	allProjects bool
}

// applyAgentInput validates in and writes it onto t. create requires name and level.
func (s *Server) applyAgentInput(r *http.Request, in *agentInput, t *db.AgentToken, create bool) map[string]string {
	errs := map[string]string{}
	if in.Name != nil || create {
		n := ""
		if in.Name != nil {
			n = strings.TrimSpace(*in.Name)
		}
		if n == "" || len([]rune(n)) > maxAgentNameLen {
			errs["name"] = "required, at most 100 characters"
		}
		t.Name = n
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len([]rune(d)) > maxAgentDescLen {
			errs["description"] = "at most 500 characters"
		}
		t.Description = d
	}
	if in.Level != nil || create {
		l := ""
		if in.Level != nil {
			l = *in.Level
		}
		if !access.ValidLevel(l) {
			errs["level"] = `must be "read", "edit" or "full"`
		}
		t.Level = l
	}
	if in.ExtraScopes != nil {
		extra := []string{}
		for _, x := range *in.ExtraScopes {
			if !access.ValidSensitive(x) {
				errs["extra_scopes"] = "unknown permission " + x
				break
			}
			if !slices.Contains(extra, x) {
				extra = append(extra, x)
			}
		}
		slices.Sort(extra)
		t.ExtraScopes = extra
	}
	if t.ExtraScopes == nil {
		t.ExtraScopes = []string{}
	}
	if len(t.ExtraScopes) > 0 && t.Level == access.LevelRead {
		errs["extra_scopes"] = "sensitive permissions need the edit or full level"
	}
	// Ceiling: only owners may hand out sensitive permissions.
	if len(t.ExtraScopes) > 0 && adminFrom(r.Context()).Role != "owner" && in.ExtraScopes != nil {
		errs["extra_scopes"] = "only an owner can grant sensitive permissions"
	}
	if in.allProjects {
		t.ProjectIDs = nil
	} else if in.ProjectIDs != nil {
		ids := []string{}
		for _, id := range *in.ProjectIDs {
			id = strings.TrimSpace(id)
			if slices.Contains(ids, id) {
				continue
			}
			if _, err := s.store.GetProject(r.Context(), id); err != nil {
				errs["project_ids"] = "unknown project " + id
				break
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 && errs["project_ids"] == "" {
			errs["project_ids"] = "list at least one project, or null for all projects"
		}
		if len(ids) > maxAgentProjects {
			errs["project_ids"] = "at most 100 projects"
		}
		t.ProjectIDs = ids
	}
	if in.ApprovalMode != nil {
		t.ApprovalMode = *in.ApprovalMode
	} else if create {
		t.ApprovalMode = "destructive"
	}
	if !slices.Contains([]string{"none", "destructive", "writes"}, t.ApprovalMode) {
		errs["approval_mode"] = `must be "none", "destructive" or "writes"`
	}
	if in.AllowedIPs != nil {
		ips := []string{}
		for _, c := range *in.AllowedIPs {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if p, err := netip.ParsePrefix(c); err == nil {
				ips = append(ips, p.Masked().String())
			} else if a, err := netip.ParseAddr(c); err == nil {
				ips = append(ips, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()).String())
			} else {
				errs["allowed_ips"] = "not an IP address or CIDR: " + c
				break
			}
		}
		if len(ips) > maxAgentIPs {
			errs["allowed_ips"] = "at most 20 entries"
		}
		t.AllowedIPs = ips
	}
	if t.AllowedIPs == nil {
		t.AllowedIPs = []string{}
	}
	if in.MaxDeletesPerHour != nil {
		if *in.MaxDeletesPerHour < 0 || *in.MaxDeletesPerHour > maxDeletesPerHourCap {
			errs["max_deletes_per_hour"] = "must be between 0 and 10000"
		}
		t.MaxDeletesPerHour = *in.MaxDeletesPerHour
	} else if create {
		t.MaxDeletesPerHour = defaultDeletesHour
	}
	if in.ExpiresInDays != nil || create {
		days := defaultAgentDays
		if in.ExpiresInDays != nil {
			days = *in.ExpiresInDays
		}
		switch {
		case days < 0 || days > maxAgentDays:
			errs["expires_in_days"] = "must be between 0 (never) and 365"
		case days == 0:
			t.ExpiresAt = nil
		default:
			exp := s.now().Add(time.Duration(days) * 24 * time.Hour)
			t.ExpiresAt = &exp
		}
	}
	return errs
}

// decodeAgentInput decodes the body and notices an explicit "project_ids": null.
func decodeAgentInput(w http.ResponseWriter, r *http.Request) (*agentInput, error) {
	var raw map[string]any
	var in agentInput
	if err := httpx.DecodeJSON(w, r, &raw); err != nil {
		return nil, err
	}
	if err := remarshal(raw, &in); err != nil {
		return nil, err
	}
	if v, ok := raw["project_ids"]; ok && v == nil {
		in.allProjects = true
	}
	return &in, nil
}

// ───────────── handlers ─────────────

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	ts, err := s.store.ListAgentTokens(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]agentDTO, 0, len(ts))
	for _, t := range ts {
		out = append(out, s.toAgentDTO(t))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[agentDTO]{Items: out})
}

func (s *Server) loadAgent(w http.ResponseWriter, r *http.Request) (*db.AgentToken, bool) {
	t, err := s.store.GetAgentToken(r.Context(), chi.URLParam(r, "agent_id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "agent")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return t, true
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	if t, ok := s.loadAgent(w, r); ok {
		httpx.WriteJSON(w, http.StatusOK, s.toAgentDTO(t))
	}
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	in, err := decodeAgentInput(w, r)
	if err != nil {
		badJSON(w, err)
		return
	}
	now := s.now()
	t := &db.AgentToken{ID: db.NewID(db.PrefixAgentToken), CreatedBy: adminFrom(r.Context()).ID, CreatedAt: now, UpdatedAt: now}
	if !in.allProjects && in.ProjectIDs == nil {
		// Omitted project_ids: all projects (same as explicit null).
		in.allProjects = true
	}
	if errs := s.applyAgentInput(r, in, t, true); len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	gen, err := auth.GenerateAgentToken(s.cfg.Pepper)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	t.Prefix, t.SecretHash = gen.Prefix, gen.Hash
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.CreateAgentToken(r.Context(), t); err != nil {
			return err
		}
		return s.audit(r, tx, "agent.create", nil, &t.ID, map[string]any{"name": t.Name, "level": t.Level,
			"extra_scopes": t.ExtraScopes, "project_ids": t.ProjectIDs, "approval_mode": t.ApprovalMode})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"agent": s.toAgentDTO(t), "token": gen.Secret, "mcp": s.mcpSetupFor(r, gen.Secret)})
}

func (s *Server) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadAgent(w, r)
	if !ok {
		return
	}
	if t.RevokedAt != nil {
		httpx.WriteProblem(w, http.StatusConflict, "revoked", "a revoked agent token cannot be changed")
		return
	}
	in, err := decodeAgentInput(w, r)
	if err != nil {
		badJSON(w, err)
		return
	}
	before := s.toAgentDTO(t)
	if errs := s.applyAgentInput(r, in, t, false); len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	t.UpdatedAt = s.now()
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.UpdateAgentToken(r.Context(), t); err != nil {
			return err
		}
		after := s.toAgentDTO(t)
		return s.audit(r, tx, "agent.update", nil, &t.ID, map[string]any{
			"level": [2]string{before.Level, after.Level}, "extra_scopes": [2][]string{before.ExtraScopes, after.ExtraScopes},
			"project_ids": [2][]string{before.ProjectIDs, after.ProjectIDs}, "approval_mode": [2]string{before.ApprovalMode, after.ApprovalMode}})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.toAgentDTO(t))
}

// handleRotateAgent replaces the secret; the old one stops working at once.
func (s *Server) handleRotateAgent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadAgent(w, r)
	if !ok {
		return
	}
	if t.RevokedAt != nil {
		httpx.WriteProblem(w, http.StatusConflict, "revoked", "a revoked agent token cannot be rotated")
		return
	}
	gen, err := auth.GenerateAgentToken(s.cfg.Pepper)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := s.now()
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.RotateAgentToken(r.Context(), t.ID, gen.Prefix, gen.Hash, now); err != nil {
			return err
		}
		return s.audit(r, tx, "agent.rotate", nil, &t.ID, map[string]any{"old_prefix": t.Prefix, "new_prefix": gen.Prefix})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	t.Prefix, t.UpdatedAt = gen.Prefix, now
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"agent": s.toAgentDTO(t), "token": gen.Secret, "mcp": s.mcpSetupFor(r, gen.Secret)})
}

func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadAgent(w, r)
	if !ok {
		return
	}
	now := s.now()
	adminID := adminFrom(r.Context()).ID
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.RevokeAgentToken(r.Context(), t.ID, now); err != nil {
			return err
		}
		if err := tx.RejectPendingApprovals(r.Context(), t.ID, adminID, "agent token revoked", now); err != nil {
			return err
		}
		return s.audit(r, tx, "agent.revoke", nil, &t.ID, map[string]any{"name": t.Name})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetAgentSettings(w http.ResponseWriter, r *http.Request) {
	enabled, err := s.agentsEnabled(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pending, err := s.store.CountPendingApprovals(r.Context(), s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	perms := map[string][]string{}
	for _, l := range access.Levels {
		perms[l] = access.Grant{Level: l}.Actions()
	}
	sensitive := make([]string, 0, len(access.Sensitive))
	for _, a := range access.Sensitive {
		sensitive = append(sensitive, string(a))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "pending_approvals": pending, "mcp_url": s.mcpURL(r),
		"levels": perms, "sensitive_scopes": sensitive})
}

func (s *Server) handlePutAgentSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	if in.Enabled == nil {
		validationFailed(w, map[string]string{"enabled": "required"})
		return
	}
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SetSetting(r.Context(), settingAgents, agentSettings{Enabled: *in.Enabled}, s.now()); err != nil {
			return err
		}
		action := "agents.disable"
		if *in.Enabled {
			action = "agents.enable"
		}
		return s.audit(r, tx, action, nil, nil, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.handleGetAgentSettings(w, r)
}

// handleAgentMe: GET /agent/me — the calling agent's identity and permissions.
func (s *Server) handleAgentMe(w http.ResponseWriter, r *http.Request) {
	t := agentFrom(r.Context())
	d := s.toAgentDTO(t)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": d.ID, "name": d.Name, "level": d.Level, "permissions": d.Permissions,
		"project_ids": d.ProjectIDs, "approval_mode": d.ApprovalMode, "max_deletes_per_hour": d.MaxDeletesPerHour, "expires_at": d.ExpiresAt})
}

// ───────────── deletion budget ─────────────

// windowCounter counts events per key in a sliding window.
type windowCounter struct {
	mu     sync.Mutex
	window time.Duration
	events map[string][]time.Time
}

func newWindowCounter(window time.Duration) *windowCounter {
	return &windowCounter{window: window, events: map[string][]time.Time{}}
}

// allow records an event for key if fewer than limit happened in the window.
func (c *windowCounter) allow(key string, limit int, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cut := now.Add(-c.window)
	ev := c.events[key]
	i := 0
	for i < len(ev) && !ev[i].After(cut) {
		i++
	}
	ev = ev[i:]
	if len(ev) >= limit {
		c.events[key] = ev
		return false
	}
	c.events[key] = append(ev, now)
	return true
}
