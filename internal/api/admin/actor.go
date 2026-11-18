package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/access"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Agent request headers (docs/AGENTS.md).
const (
	// HeaderAgentReason carries the agent's justification of a change. It is
	// required on every state-changing agent request and lands in the audit log.
	HeaderAgentReason = "X-Kadr-Agent-Reason"
	// HeaderAgentTool names the MCP tool that issued the request (informational).
	HeaderAgentTool = "X-Kadr-Agent-Tool"
)

const (
	minReasonLen = 3
	maxReasonLen = 500
	// agentTouchEvery throttles last_used_at writes.
	agentTouchEvery = time.Minute
)

// settingAgents is the settings key of the global agent switch.
const settingAgents = "agents"

type agentSettings struct {
	Enabled bool `json:"enabled"`
}

// agentsEnabled reads the global kill switch (default: enabled).
func (s *Server) agentsEnabled(ctx context.Context) (bool, error) {
	var st agentSettings
	err := s.store.GetSetting(ctx, settingAgents, &st)
	if errors.Is(err, db.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return st.Enabled, nil
}

const (
	ctxAgent ctxKey = iota + 100
	ctxApproved
)

func agentFrom(ctx context.Context) *db.AgentToken {
	t, _ := ctx.Value(ctxAgent).(*db.AgentToken)
	return t
}

// approvedFrom returns the id of the approval being executed, if any.
func approvedFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxApproved).(string)
	return id
}

// actorID returns the id of the admin or agent behind the request.
func actorID(ctx context.Context) string {
	if a := adminFrom(ctx); a != nil {
		return a.ID
	}
	if t := agentFrom(ctx); t != nil {
		return t.ID
	}
	return ""
}

// grantOf returns the permission set of an agent token.
func grantOf(t *db.AgentToken) access.Grant {
	return access.Grant{Level: t.Level, Extra: t.ExtraScopes}
}

// requireActor authenticates the caller: an agent token in
// "Authorization: Bearer kd_agent_…", or else the admin session cookie.
// Requests dispatched internally (approved agent actions) already carry the
// agent in their context.
func (s *Server) requireActor(next http.Handler) http.Handler {
	session := s.requireSession(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if agentFrom(r.Context()) != nil && (approvedFrom(r.Context()) != "" || internalFrom(r.Context())) {
			next.ServeHTTP(w, r)
			return
		}
		tok, ok := bearerToken(r)
		if !ok {
			session.ServeHTTP(w, r)
			return
		}
		t, err := s.authenticateAgent(r, tok)
		if err != nil {
			var p *problem
			if errors.As(err, &p) {
				if p.status == http.StatusUnauthorized {
					w.Header().Set("WWW-Authenticate", `Bearer realm="kadr", error="invalid_token"`)
				}
				httpx.WriteProblem(w, p.status, p.code, p.detail)
				return
			}
			s.internalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAgent, t)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// problem is an error that maps to an RFC 9457 response.
type problem struct {
	status       int
	code, detail string
}

func (p *problem) Error() string { return p.code + ": " + p.detail }

var errAgentToken = &problem{http.StatusUnauthorized, "unauthenticated", "invalid, expired or revoked agent token"}

// authenticateAgent resolves an agent token. Failures are deliberately not
// distinguished (except the global switch and the IP allowlist, which the
// token holder is entitled to learn about).
func (s *Server) authenticateAgent(r *http.Request, secret string) (*db.AgentToken, error) {
	ctx := r.Context()
	prefix, err := auth.ParseAgentToken(secret)
	if err != nil {
		return nil, errAgentToken
	}
	t, err := s.store.GetAgentTokenByPrefix(ctx, prefix)
	if errors.Is(err, db.ErrNotFound) {
		_ = auth.HashAPIKey(secret, s.cfg.Pepper) // uniform timing
		return nil, errAgentToken
	}
	if err != nil {
		return nil, err
	}
	if !auth.EqualConstantTime(string(auth.HashAPIKey(secret, s.cfg.Pepper)), string(t.SecretHash)) {
		return nil, errAgentToken
	}
	now := s.now()
	if !t.Active(now) {
		return nil, errAgentToken
	}
	enabled, err := s.agentsEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, &problem{http.StatusForbidden, "agents_disabled", "agent access is switched off by an administrator"}
	}
	ip := httpx.ClientIP(r)
	if !ipAllowed(t.AllowedIPs, ip) {
		return nil, &problem{http.StatusForbidden, "ip_not_allowed", "this agent token cannot be used from " + ip}
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= agentTouchEvery || (t.LastUsedIP != nil && *t.LastUsedIP != ip) {
		if err := s.store.TouchAgentToken(ctx, t.ID, ip, now); err != nil {
			s.log.WarnContext(ctx, "touch agent token failed", "err", err)
		}
	}
	return t, nil
}

// ipAllowed reports whether ip matches one of the CIDRs (empty list = any).
func ipAllowed(cidrs []string, ip string) bool {
	if len(cidrs) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}

// agentReason returns the trimmed reason header of an agent request.
func agentReason(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(HeaderAgentReason))
}

// withReason merges the agent's reason (and tool) into audit details.
func withReason(r *http.Request, details any) any {
	reason := agentReason(r)
	tool := strings.TrimSpace(r.Header.Get(HeaderAgentTool))
	m := map[string]any{}
	switch d := details.(type) {
	case nil:
	case map[string]any:
		for k, v := range d {
			m[k] = v
		}
	default:
		b, err := json.Marshal(d)
		if err != nil || json.Unmarshal(b, &m) != nil {
			m = map[string]any{"data": d}
		}
	}
	if reason != "" {
		m["reason"] = reason
	}
	if tool != "" {
		m["tool"] = tool
	}
	if id := approvedFrom(r.Context()); id != "" {
		m["approval_id"] = id
	}
	return m
}

// remarshal converts a decoded JSON value into dst.
func remarshal(v any, dst any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
