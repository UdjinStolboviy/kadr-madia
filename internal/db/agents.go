package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ID prefixes of the agent tables.
const (
	PrefixAgentToken    = "agt"
	PrefixAgentApproval = "apr"
)

// Agent approval statuses.
const (
	ApprovalPending  = "pending"
	ApprovalExecuted = "executed"
	ApprovalFailed   = "failed"
	ApprovalRejected = "rejected"
	ApprovalExpired  = "expired"
)

// AgentToken is an access token issued to an AI agent (docs/AGENTS.md).
// Only the peppered hash of the secret is stored.
type AgentToken struct {
	ID                string
	Name              string
	Description       string
	Prefix            string
	SecretHash        []byte
	Level             string   // read | edit | full
	ExtraScopes       []string // sensitive actions granted explicitly
	ProjectIDs        []string // nil = all projects
	ApprovalMode      string   // none | destructive | writes
	AllowedIPs        []string // CIDRs; empty = any address
	MaxDeletesPerHour int
	CreatedBy         string
	OAuthClientID     *string // set when the token was obtained through OAuth
	LastUsedAt        *time.Time
	LastUsedIP        *string
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// AllProjects reports whether the token is not restricted to a project list.
func (t *AgentToken) AllProjects() bool { return t.ProjectIDs == nil }

// CoversProject reports whether the token may act on projectID.
func (t *AgentToken) CoversProject(projectID string) bool {
	if t.ProjectIDs == nil {
		return true
	}
	for _, id := range t.ProjectIDs {
		if id == projectID {
			return true
		}
	}
	return false
}

// Active reports whether the token can authenticate at now.
func (t *AgentToken) Active(now time.Time) bool {
	return t.RevokedAt == nil && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt))
}

const agentTokenCols = `id, name, description, prefix, secret_hash, level, extra_scopes, project_ids, approval_mode,
	allowed_ips, max_deletes_per_hour, created_by, oauth_client_id, last_used_at, last_used_ip, expires_at, revoked_at, created_at, updated_at`

func scanAgentToken(row interface{ Scan(...any) error }) (*AgentToken, error) {
	var t AgentToken
	var extra, ips string
	var projects, lastIP, oauthClient sql.NullString
	var last, exp, rev sql.NullInt64
	var created, updated int64
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Prefix, &t.SecretHash, &t.Level, &extra, &projects, &t.ApprovalMode,
		&ips, &t.MaxDeletesPerHour, &t.CreatedBy, &oauthClient, &last, &lastIP, &exp, &rev, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(extra), &t.ExtraScopes); err != nil {
		return nil, fmt.Errorf("agent token %s: extra_scopes: %w", t.ID, err)
	}
	if err := json.Unmarshal([]byte(ips), &t.AllowedIPs); err != nil {
		return nil, fmt.Errorf("agent token %s: allowed_ips: %w", t.ID, err)
	}
	if projects.Valid {
		t.ProjectIDs = []string{}
		if err := json.Unmarshal([]byte(projects.String), &t.ProjectIDs); err != nil {
			return nil, fmt.Errorf("agent token %s: project_ids: %w", t.ID, err)
		}
	}
	if t.ExtraScopes == nil {
		t.ExtraScopes = []string{}
	}
	if t.AllowedIPs == nil {
		t.AllowedIPs = []string{}
	}
	t.LastUsedIP, t.OAuthClientID = fromNullStr(lastIP), fromNullStr(oauthClient)
	t.LastUsedAt, t.ExpiresAt, t.RevokedAt = fromNullMs(last), fromNullMs(exp), fromNullMs(rev)
	t.CreatedAt, t.UpdatedAt = fromMs(created), fromMs(updated)
	return &t, nil
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func nullJSONList(v []string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: jsonList(v), Valid: true}
}

// CreateAgentToken inserts a token.
func (s *Store) CreateAgentToken(ctx context.Context, t *AgentToken) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO agent_tokens (id, name, description, prefix, secret_hash, level, extra_scopes, project_ids, approval_mode,
		   allowed_ips, max_deletes_per_hour, created_by, oauth_client_id, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Description, t.Prefix, t.SecretHash, t.Level, jsonList(t.ExtraScopes), nullJSONList(t.ProjectIDs),
		t.ApprovalMode, jsonList(t.AllowedIPs), t.MaxDeletesPerHour, t.CreatedBy, nullStr(t.OAuthClientID), nullMs(t.ExpiresAt), ms(t.CreatedAt), ms(t.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create agent token: %w", mapErr(err))
	}
	return nil
}

// UpdateAgentToken rewrites the mutable fields of a token (not the secret).
func (s *Store) UpdateAgentToken(ctx context.Context, t *AgentToken) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE agent_tokens SET name = ?, description = ?, level = ?, extra_scopes = ?, project_ids = ?, approval_mode = ?,
		   allowed_ips = ?, max_deletes_per_hour = ?, expires_at = ?, updated_at = ? WHERE id = ?`,
		t.Name, t.Description, t.Level, jsonList(t.ExtraScopes), nullJSONList(t.ProjectIDs), t.ApprovalMode,
		jsonList(t.AllowedIPs), t.MaxDeletesPerHour, nullMs(t.ExpiresAt), ms(t.UpdatedAt), t.ID)
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}

// ListAgentTokens returns every token, newest first (revoked included).
func (s *Store) ListAgentTokens(ctx context.Context) ([]*AgentToken, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+agentTokenCols+` FROM agent_tokens ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*AgentToken{}
	for rows.Next() {
		t, err := scanAgentToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapErr(rows.Err())
}

// GetAgentToken returns a token by id.
func (s *Store) GetAgentToken(ctx context.Context, id string) (*AgentToken, error) {
	return scanAgentToken(s.r.QueryRowContext(ctx, `SELECT `+agentTokenCols+` FROM agent_tokens WHERE id = ?`, id))
}

// GetAgentTokenByPrefix returns a token by its public prefix.
func (s *Store) GetAgentTokenByPrefix(ctx context.Context, prefix string) (*AgentToken, error) {
	return scanAgentToken(s.r.QueryRowContext(ctx, `SELECT `+agentTokenCols+` FROM agent_tokens WHERE prefix = ?`, prefix))
}

// RevokeAgentToken sets revoked_at (idempotent).
func (s *Store) RevokeAgentToken(ctx context.Context, id string, now time.Time) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE agent_tokens SET revoked_at = COALESCE(revoked_at, ?), refresh_hash = NULL, updated_at = ? WHERE id = ?`, ms(now), ms(now), id)
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}

// TouchAgentToken records the last use of a token.
func (s *Store) TouchAgentToken(ctx context.Context, id, ip string, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE agent_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, ms(now), ip, id)
	return mapErr(err)
}

// ───────────── approvals ─────────────

// AgentApproval is an agent request waiting for (or decided by) a human.
type AgentApproval struct {
	ID           string
	TokenID      string
	ProjectID    *string
	Action       string
	Tool         *string
	Method       string
	Path         string
	Body         []byte // encrypted envelope
	ContentType  *string
	RequestHash  string
	Reason       string
	Status       string
	ResultStatus *int64
	ResultBody   []byte // encrypted envelope
	DecidedBy    *string
	DecisionNote *string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	DecidedAt    *time.Time
}

const approvalCols = `id, token_id, project_id, action, tool, method, path, body, content_type, request_hash, reason, status,
	result_status, result_body, decided_by, decision_note, created_at, expires_at, decided_at`

func scanApproval(row interface{ Scan(...any) error }) (*AgentApproval, error) {
	var a AgentApproval
	var project, tool, ct, decidedBy, note sql.NullString
	var resStatus, decided sql.NullInt64
	var created, expires int64
	if err := row.Scan(&a.ID, &a.TokenID, &project, &a.Action, &tool, &a.Method, &a.Path, &a.Body, &ct, &a.RequestHash, &a.Reason, &a.Status,
		&resStatus, &a.ResultBody, &decidedBy, &note, &created, &expires, &decided); err != nil {
		return nil, mapErr(err)
	}
	a.ProjectID, a.Tool, a.ContentType = fromNullStr(project), fromNullStr(tool), fromNullStr(ct)
	a.DecidedBy, a.DecisionNote = fromNullStr(decidedBy), fromNullStr(note)
	a.ResultStatus = fromNullInt(resStatus)
	a.CreatedAt, a.ExpiresAt, a.DecidedAt = fromMs(created), fromMs(expires), fromNullMs(decided)
	return &a, nil
}

// CreateAgentApproval inserts a pending approval.
func (s *Store) CreateAgentApproval(ctx context.Context, a *AgentApproval) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO agent_approvals (id, token_id, project_id, action, tool, method, path, body, content_type, request_hash, reason, status, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TokenID, nullStr(a.ProjectID), a.Action, nullStr(a.Tool), a.Method, a.Path, a.Body, nullStr(a.ContentType),
		a.RequestHash, a.Reason, a.Status, ms(a.CreatedAt), ms(a.ExpiresAt))
	if err != nil {
		return fmt.Errorf("create agent approval: %w", mapErr(err))
	}
	return nil
}

// GetAgentApproval returns an approval by id.
func (s *Store) GetAgentApproval(ctx context.Context, id string) (*AgentApproval, error) {
	return scanApproval(s.r.QueryRowContext(ctx, `SELECT `+approvalCols+` FROM agent_approvals WHERE id = ?`, id))
}

// FindPendingApproval returns a pending, unexpired approval of the token for
// the same request (by hash), so a retrying agent does not flood the queue.
func (s *Store) FindPendingApproval(ctx context.Context, tokenID, requestHash string, now time.Time) (*AgentApproval, error) {
	return scanApproval(s.r.QueryRowContext(ctx,
		`SELECT `+approvalCols+` FROM agent_approvals
		  WHERE token_id = ? AND status = 'pending' AND request_hash = ? AND expires_at > ?
		  ORDER BY created_at DESC LIMIT 1`, tokenID, requestHash, ms(now)))
}

// ApprovalFilter selects approvals.
type ApprovalFilter struct {
	Status  string
	TokenID string
	Limit   int
}

// ListAgentApprovals returns approvals newest first.
func (s *Store) ListAgentApprovals(ctx context.Context, f ApprovalFilter) ([]*AgentApproval, error) {
	var where []string
	var args []any
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.TokenID != "" {
		where = append(where, "token_id = ?")
		args = append(args, f.TokenID)
	}
	q := `SELECT ` + approvalCols + ` FROM agent_approvals`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*AgentApproval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// RejectPendingApprovals rejects every pending approval of a token (on revoke).
func (s *Store) RejectPendingApprovals(ctx context.Context, tokenID, adminID, note string, now time.Time) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE agent_approvals SET status = 'rejected', decided_by = ?, decision_note = ?, decided_at = ?
		  WHERE token_id = ? AND status = 'pending'`, adminID, note, ms(now), tokenID)
	return mapErr(err)
}

// CountPendingApprovals counts unexpired pending approvals.
func (s *Store) CountPendingApprovals(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM agent_approvals WHERE status = 'pending' AND expires_at > ?`, ms(now)).Scan(&n)
	return n, mapErr(err)
}

// ExpireAgentApprovals marks overdue pending approvals expired.
func (s *Store) ExpireAgentApprovals(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`UPDATE agent_approvals SET status = 'expired', decided_at = ? WHERE status = 'pending' AND expires_at <= ?`, ms(now), ms(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// ClaimAgentApproval moves a pending approval to a decided status. It returns
// ErrNotFound when the approval is no longer pending (decided concurrently or
// expired), so exactly one decision wins.
func (s *Store) ClaimAgentApproval(ctx context.Context, id, status, adminID string, note *string, now time.Time) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE agent_approvals SET status = ?, decided_by = ?, decision_note = ?, decided_at = ?
		  WHERE id = ? AND status = 'pending' AND expires_at > ?`, status, adminID, nullStr(note), ms(now), id, ms(now))
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}

// SetAgentApprovalResult stores the outcome of an executed approval.
func (s *Store) SetAgentApprovalResult(ctx context.Context, id, status string, code int, body []byte) error {
	_, err := s.w.ExecContext(ctx, `UPDATE agent_approvals SET status = ?, result_status = ?, result_body = ? WHERE id = ?`,
		status, code, body, id)
	return mapErr(err)
}

// ───────────── moderation ─────────────

// Moderation statuses. An asset without a row is "unreviewed".
const (
	ModerationApproved = "approved"
	ModerationFlagged  = "flagged"
	ModerationRejected = "rejected"
)

// AssetModeration is the moderation decision on one asset.
type AssetModeration struct {
	AssetID   string
	ProjectID string
	Status    string
	Labels    []string
	Note      *string
	ActorType string
	ActorID   *string
	UpdatedAt time.Time
}

const moderationCols = `asset_id, project_id, status, labels, note, actor_type, actor_id, updated_at`

func scanModeration(row interface{ Scan(...any) error }) (*AssetModeration, error) {
	var m AssetModeration
	var labels string
	var note, actor sql.NullString
	var updated int64
	if err := row.Scan(&m.AssetID, &m.ProjectID, &m.Status, &labels, &note, &m.ActorType, &actor, &updated); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(labels), &m.Labels); err != nil {
		return nil, fmt.Errorf("moderation %s: labels: %w", m.AssetID, err)
	}
	if m.Labels == nil {
		m.Labels = []string{}
	}
	m.Note, m.ActorID = fromNullStr(note), fromNullStr(actor)
	m.UpdatedAt = fromMs(updated)
	return &m, nil
}

// UpsertModeration stores the moderation decision of an asset.
func (s *Store) UpsertModeration(ctx context.Context, m *AssetModeration) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO asset_moderation (asset_id, project_id, status, labels, note, actor_type, actor_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(asset_id) DO UPDATE SET status = excluded.status, labels = excluded.labels, note = excluded.note,
		   actor_type = excluded.actor_type, actor_id = excluded.actor_id, updated_at = excluded.updated_at`,
		m.AssetID, m.ProjectID, m.Status, jsonList(m.Labels), nullStr(m.Note), m.ActorType, nullStr(m.ActorID), ms(m.UpdatedAt))
	return mapErr(err)
}

// GetModeration returns the moderation decision of an asset.
func (s *Store) GetModeration(ctx context.Context, assetID string) (*AssetModeration, error) {
	return scanModeration(s.r.QueryRowContext(ctx, `SELECT `+moderationCols+` FROM asset_moderation WHERE asset_id = ?`, assetID))
}

// ModerationQueueItem is one entry of the moderation queue.
type ModerationQueueItem struct {
	AssetID    string
	Kind       string
	Namespace  string
	Visibility string
	CreatedAt  time.Time
	Moderation *AssetModeration // nil = unreviewed
}

// ModerationFilter selects queue entries. Status is "unreviewed" (default),
// "approved", "flagged", "rejected" or "all". Cursor is the asset id of the
// last item of the previous page (assets are listed newest first).
type ModerationFilter struct {
	ProjectID string
	Status    string
	Kind      string
	Cursor    string
	Limit     int
}

// ListModerationQueue lists ready assets of a project with their moderation state.
func (s *Store) ListModerationQueue(ctx context.Context, f ModerationFilter) ([]*ModerationQueueItem, error) {
	q := `SELECT a.id, a.kind, a.ns, a.visibility, a.created_at,
	             m.asset_id, m.project_id, m.status, m.labels, m.note, m.actor_type, m.actor_id, m.updated_at
	        FROM assets a LEFT JOIN asset_moderation m ON m.asset_id = a.id
	       WHERE a.project_id = ? AND a.status = 'ready'`
	args := []any{f.ProjectID}
	switch f.Status {
	case "", "unreviewed":
		q += ` AND m.asset_id IS NULL`
	case "all":
	default:
		q += ` AND m.status = ?`
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		q += ` AND a.kind = ?`
		args = append(args, f.Kind)
	}
	if f.Cursor != "" {
		q += ` AND a.id < ?`
		args = append(args, f.Cursor)
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	q += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*ModerationQueueItem{}
	for rows.Next() {
		var it ModerationQueueItem
		var created int64
		var mAsset, mProject, mStatus, mLabels, mNote, mActorType, mActorID sql.NullString
		var mUpdated sql.NullInt64
		if err := rows.Scan(&it.AssetID, &it.Kind, &it.Namespace, &it.Visibility, &created,
			&mAsset, &mProject, &mStatus, &mLabels, &mNote, &mActorType, &mActorID, &mUpdated); err != nil {
			return nil, mapErr(err)
		}
		it.CreatedAt = fromMs(created)
		if mAsset.Valid {
			m := &AssetModeration{AssetID: mAsset.String, ProjectID: mProject.String, Status: mStatus.String,
				Note: fromNullStr(mNote), ActorType: mActorType.String, ActorID: fromNullStr(mActorID), UpdatedAt: fromMs(mUpdated.Int64)}
			if err := json.Unmarshal([]byte(mLabels.String), &m.Labels); err != nil || m.Labels == nil {
				m.Labels = []string{}
			}
			it.Moderation = m
		}
		out = append(out, &it)
	}
	return out, mapErr(rows.Err())
}

// mustAffect turns "no row updated" into ErrNotFound.
func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateAgentToken replaces the prefix and secret of a token.
func (s *Store) RotateAgentToken(ctx context.Context, id, prefix string, hash []byte, now time.Time) error {
	res, err := s.w.ExecContext(ctx, `UPDATE agent_tokens SET prefix = ?, secret_hash = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL`,
		prefix, hash, ms(now), id)
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}
