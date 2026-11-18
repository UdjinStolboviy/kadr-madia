package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

const (
	approvalTTL          = 24 * time.Hour
	maxApprovalBodyBytes = 1 << 20  // requests held for approval
	maxResultBytes       = 64 << 10 // stored outcome of an executed approval
	approvalExecTimeout  = 2 * time.Minute
)

// holdForApproval stores the request and answers 202: a human has to approve
// it in the admin before it runs (with the agent's identity and permissions
// re-checked at that moment).
func (s *Server) holdForApproval(w http.ResponseWriter, r *http.Request, t *db.AgentToken, rt route, projectID string) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxApprovalBodyBytes+1))
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if len(body) > maxApprovalBodyBytes {
		httpx.WriteProblem(w, http.StatusRequestEntityTooLarge, "too_large", "requests larger than 1 MB cannot be held for approval")
		return
	}
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n%s\n", r.Method, path)
	sum.Write(body)
	hash := hex.EncodeToString(sum.Sum(nil))
	now := s.now()

	if a, err := s.store.FindPendingApproval(ctx, t.ID, hash, now); err == nil {
		s.writePending(w, a)
		return
	} else if !errors.Is(err, db.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}

	a := &db.AgentApproval{ID: db.NewID(db.PrefixAgentApproval), TokenID: t.ID, Action: string(rt.action), Method: r.Method,
		Path: path, RequestHash: hash, Reason: agentReason(r), Status: db.ApprovalPending, CreatedAt: now, ExpiresAt: now.Add(approvalTTL)}
	if projectID != "" {
		a.ProjectID = &projectID
	}
	if tool := strings.TrimSpace(r.Header.Get(HeaderAgentTool)); tool != "" {
		a.Tool = &tool
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		a.ContentType = &ct
	}
	if len(body) > 0 {
		if a.Body, err = s.cfg.Keyring.Encrypt(body, approvalAAD(a.ID, "request")); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateAgentApproval(ctx, a); err != nil {
			return err
		}
		return s.audit(r, tx, "agent.approval.request", a.ProjectID, &a.ID, map[string]any{"action": a.Action, "method": a.Method, "path": a.Path})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.notifyApproval(t, a)
	s.writePending(w, a)
}

func (s *Server) writePending(w http.ResponseWriter, a *db.AgentApproval) {
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"status":      "pending_approval",
		"approval_id": a.ID,
		"expires_at":  ts(a.ExpiresAt),
		"detail": "This action needs a human decision. An administrator can approve or reject it in the Kadr admin " +
			"(AI agents → Approvals). Check the outcome later with GET /admin/api/agent/approvals/" + a.ID + ".",
	})
}

func approvalAAD(id, part string) []byte { return []byte("agent-approval:" + id + ":" + part) }

// notifyApproval pings operators (best effort, asynchronous).
func (s *Server) notifyApproval(t *db.AgentToken, a *db.AgentApproval) {
	if s.cfg.Notifier == nil {
		return
	}
	text := fmt.Sprintf("🤖 Kadr: agent %q asks for approval\n%s %s\nPermission: %s\nReason: %s",
		t.Name, a.Method, strings.TrimPrefix(a.Path, "/admin/api"), a.Action, a.Reason)
	if s.cfg.PublicURL != "" {
		text += "\n" + strings.TrimRight(s.cfg.PublicURL, "/") + "/agents?tab=approvals"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.cfg.Notifier.Notify(ctx, text); err != nil {
			s.log.Warn("approval notification failed", "err", err)
		}
	}()
}

// ───────────── DTO ─────────────

type approvalDTO struct {
	ID           string          `json:"id"`
	AgentID      string          `json:"agent_id"`
	AgentName    string          `json:"agent_name,omitempty"`
	ProjectID    *string         `json:"project_id"`
	Action       string          `json:"action"`
	Tool         *string         `json:"tool"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Body         json.RawMessage `json:"body"`
	Reason       string          `json:"reason"`
	Status       string          `json:"status"`
	ResultStatus *int64          `json:"result_status"`
	Result       json.RawMessage `json:"result"`
	DecidedBy    *string         `json:"decided_by"`
	DecisionNote *string         `json:"decision_note"`
	CreatedAt    string          `json:"created_at"`
	ExpiresAt    string          `json:"expires_at"`
	DecidedAt    *string         `json:"decided_at"`
}

// toApprovalDTO decrypts the request and result. With mask, values of
// secret-looking fields are hidden (the admin view); the agent sees its own
// result in full (e.g. an API key it asked for).
func (s *Server) toApprovalDTO(a *db.AgentApproval, agentName string, mask bool) approvalDTO {
	d := approvalDTO{ID: a.ID, AgentID: a.TokenID, AgentName: agentName, ProjectID: a.ProjectID, Action: a.Action, Tool: a.Tool,
		Method: a.Method, Path: strings.TrimPrefix(a.Path, "/admin/api"), Reason: a.Reason, Status: a.Status, ResultStatus: a.ResultStatus,
		DecidedBy: a.DecidedBy, DecisionNote: a.DecisionNote, CreatedAt: ts(a.CreatedAt), ExpiresAt: ts(a.ExpiresAt), DecidedAt: tsp(a.DecidedAt),
		Body: json.RawMessage("null"), Result: json.RawMessage("null")}
	if d.Status == db.ApprovalPending && !s.now().Before(a.ExpiresAt) {
		d.Status = db.ApprovalExpired
	}
	if len(a.Body) > 0 {
		d.Body = s.decryptJSON(a.Body, approvalAAD(a.ID, "request"), true, a.ContentType)
	}
	if len(a.ResultBody) > 0 {
		d.Result = s.decryptJSON(a.ResultBody, approvalAAD(a.ID, "result"), mask, nil)
	}
	return d
}

func (s *Server) decryptJSON(blob, aad []byte, mask bool, contentType *string) json.RawMessage {
	plain, err := s.cfg.Keyring.Decrypt(blob, aad)
	if err != nil {
		return json.RawMessage(`"(undecryptable)"`)
	}
	if contentType != nil && !strings.HasPrefix(*contentType, "application/json") {
		return mustJSON(fmt.Sprintf("(%d bytes of %s)", len(plain), *contentType))
	}
	var v any
	if json.Unmarshal(plain, &v) != nil {
		return mustJSON(string(plain))
	}
	if mask {
		v = maskSecrets(v)
	}
	return mustJSON(v)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

var secretKeyRe = regexp.MustCompile(`(?i)(secret|password|token|private|credential|access_key)`)

// maskSecrets replaces string values of secret-looking keys.
func maskSecrets(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && s != "" && secretKeyRe.MatchString(k) {
				x[k] = "••••••"
				continue
			}
			x[k] = maskSecrets(val)
		}
	case []any:
		for i := range x {
			x[i] = maskSecrets(x[i])
		}
	}
	return v
}

// ───────────── admin handlers ─────────────

func (s *Server) agentNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if ts, err := s.store.ListAgentTokens(ctx); err == nil {
		for _, t := range ts {
			names[t.ID] = t.Name
		}
	}
	return names
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request, f db.ApprovalFilter, mask bool) {
	if _, err := s.store.ExpireAgentApprovals(r.Context(), s.now()); err != nil {
		s.log.WarnContext(r.Context(), "expire approvals failed", "err", err)
	}
	as, err := s.store.ListAgentApprovals(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names := s.agentNames(r.Context())
	out := make([]approvalDTO, 0, len(as))
	for _, a := range as {
		out = append(out, s.toApprovalDTO(a, names[a.TokenID], mask))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[approvalDTO]{Items: out})
}

var approvalStatuses = []string{"", db.ApprovalPending, db.ApprovalExecuted, db.ApprovalFailed, db.ApprovalRejected, db.ApprovalExpired}

// handleListApprovals: GET /agent-approvals?status=&agent_id=&limit=
func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st := q.Get("status")
	if !containsStr(approvalStatuses, st) {
		validationFailed(w, map[string]string{"status": "unknown status"})
		return
	}
	limit := atoiDefault(q.Get("limit"), 100)
	s.listApprovals(w, r, db.ApprovalFilter{Status: st, TokenID: q.Get("agent_id"), Limit: limit}, true)
}

func (s *Server) loadApproval(w http.ResponseWriter, r *http.Request) (*db.AgentApproval, bool) {
	a, err := s.store.GetAgentApproval(r.Context(), chi.URLParam(r, "approval_id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "approval")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return a, true
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadApproval(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.toApprovalDTO(a, s.agentNames(r.Context())[a.TokenID], true))
}

// handleApprove runs the held request as the agent and records the outcome.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadApproval(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = httpx.DecodeJSON(w, r, &in) // the body is optional
	ctx := r.Context()
	adminID := adminFrom(ctx).ID
	t, err := s.store.GetAgentToken(ctx, a.TokenID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := s.now()
	if !t.Active(now) {
		httpx.WriteProblem(w, http.StatusConflict, "agent_inactive", "the agent token was revoked or has expired; reject the request instead")
		return
	}
	var note *string
	if n := strings.TrimSpace(in.Note); n != "" {
		note = &n
	}
	if err := s.store.ClaimAgentApproval(ctx, a.ID, db.ApprovalExecuted, adminID, note, now); errors.Is(err, db.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusConflict, "not_pending", "the request was already decided or has expired")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "agent.approval.approve", a.ProjectID, &a.ID, map[string]any{"agent_id": t.ID, "action": a.Action, "path": a.Path})

	var body []byte
	if len(a.Body) > 0 {
		if body, err = s.cfg.Keyring.Decrypt(a.Body, approvalAAD(a.ID, "request")); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	code, result := s.replay(t, a, body)
	status := db.ApprovalExecuted
	if code >= 400 {
		status = db.ApprovalFailed
	}
	if len(result) > maxResultBytes {
		result = mustJSON(map[string]any{"truncated": true, "bytes": len(result)})
	}
	var enc []byte
	if len(result) > 0 {
		if enc, err = s.cfg.Keyring.Encrypt(result, approvalAAD(a.ID, "result")); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if err := s.store.SetAgentApprovalResult(ctx, a.ID, status, code, enc); err != nil {
		s.internalError(w, r, err)
		return
	}
	a, err = s.store.GetAgentApproval(ctx, a.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.toApprovalDTO(a, t.Name, true))
}

// replay dispatches the held request through the full admin handler as the
// agent. Authorization runs again, so a token whose permissions were reduced
// meanwhile is refused.
func (s *Server) replay(t *db.AgentToken, a *db.AgentApproval, body []byte) (int, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), approvalExecTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, ctxAgent, t)
	ctx = context.WithValue(ctx, ctxApproved, a.ID)
	req, err := http.NewRequestWithContext(ctx, a.Method, a.Path, bytes.NewReader(body))
	if err != nil {
		return http.StatusInternalServerError, mustJSON(map[string]string{"error": err.Error()})
	}
	req.RemoteAddr = "127.0.0.1:0"
	if a.ContentType != nil {
		req.Header.Set("Content-Type", *a.ContentType)
	}
	req.Header.Set(HeaderAgentReason, a.Reason)
	if a.Tool != nil {
		req.Header.Set(HeaderAgentTool, *a.Tool)
	}
	rec := newRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.code, rec.body.Bytes()
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadApproval(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = httpx.DecodeJSON(w, r, &in)
	var note *string
	if n := strings.TrimSpace(in.Note); n != "" {
		note = &n
	}
	ctx := r.Context()
	if err := s.store.ClaimAgentApproval(ctx, a.ID, db.ApprovalRejected, adminFrom(ctx).ID, note, s.now()); errors.Is(err, db.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusConflict, "not_pending", "the request was already decided or has expired")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "agent.approval.reject", a.ProjectID, &a.ID, map[string]any{"agent_id": a.TokenID, "note": in.Note})
	a, err := s.store.GetAgentApproval(ctx, a.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.toApprovalDTO(a, s.agentNames(ctx)[a.TokenID], true))
}

// ───────────── agent self-service ─────────────

// handleAgentApprovals: GET /agent/approvals — the calling agent's requests.
func (s *Server) handleAgentApprovals(w http.ResponseWriter, r *http.Request) {
	t := agentFrom(r.Context())
	s.listApprovals(w, r, db.ApprovalFilter{TokenID: t.ID, Status: r.URL.Query().Get("status"), Limit: 50}, true)
}

// handleAgentApproval: GET /agent/approvals/{id} — status and, once executed,
// the full result of one of the agent's own requests.
func (s *Server) handleAgentApproval(w http.ResponseWriter, r *http.Request) {
	t := agentFrom(r.Context())
	a, err := s.store.GetAgentApproval(r.Context(), chi.URLParam(r, "approval_id"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && a.TokenID != t.ID) {
		notFound(w, "approval")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.toApprovalDTO(a, t.Name, false))
}

// ───────────── helpers ─────────────

// recorder is a minimal in-memory ResponseWriter for internal dispatch.
type recorder struct {
	hdr  http.Header
	code int
	body bytes.Buffer
}

func newRecorder() *recorder { return &recorder{hdr: http.Header{}, code: http.StatusOK} }

func (r *recorder) Header() http.Header         { return r.hdr }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.code = code }
func (r *recorder) Flush()                      {}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func atoiDefault(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}
