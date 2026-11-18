package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ID prefixes of the OAuth tables.
const (
	PrefixOAuthClient = "oac"
	PrefixOAuthGrant  = "oag"
)

// OAuthClient is a dynamically registered public client (RFC 7591).
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedIP    *string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

// CreateOAuthClient registers a client.
func (s *Store) CreateOAuthClient(ctx context.Context, c *OAuthClient) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris, created_ip, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.Name, jsonList(c.RedirectURIs), nullStr(c.CreatedIP), ms(c.CreatedAt))
	return mapErr(err)
}

// GetOAuthClient returns a client by id.
func (s *Store) GetOAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	var c OAuthClient
	var uris string
	var ip sql.NullString
	var created int64
	var last sql.NullInt64
	err := s.r.QueryRowContext(ctx, `SELECT id, name, redirect_uris, created_ip, created_at, last_used_at FROM oauth_clients WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &uris, &ip, &created, &last)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, fmt.Errorf("oauth client %s: redirect_uris: %w", id, err)
	}
	c.CreatedIP, c.CreatedAt, c.LastUsedAt = fromNullStr(ip), fromMs(created), fromNullMs(last)
	return &c, nil
}

// TouchOAuthClient records the last token exchange of a client.
func (s *Store) TouchOAuthClient(ctx context.Context, id string, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE oauth_clients SET last_used_at = ? WHERE id = ?`, ms(now), id)
	return mapErr(err)
}

// OAuthGrant is an authorization request and, once approved, its code.
type OAuthGrant struct {
	ID            string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	State         *string
	Scope         *string
	Resource      *string
	Status        string // pending | approved | denied | used
	CodeHash      []byte
	AgentTokenID  *string
	DecidedBy     *string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

const grantCols = `id, client_id, redirect_uri, code_challenge, state, scope, resource, status, code_hash, agent_token_id, decided_by, created_at, expires_at`

func scanGrant(row interface{ Scan(...any) error }) (*OAuthGrant, error) {
	var g OAuthGrant
	var state, scope, resource, agent, decided sql.NullString
	var created, expires int64
	if err := row.Scan(&g.ID, &g.ClientID, &g.RedirectURI, &g.CodeChallenge, &state, &scope, &resource, &g.Status, &g.CodeHash,
		&agent, &decided, &created, &expires); err != nil {
		return nil, mapErr(err)
	}
	g.State, g.Scope, g.Resource, g.AgentTokenID, g.DecidedBy = fromNullStr(state), fromNullStr(scope), fromNullStr(resource), fromNullStr(agent), fromNullStr(decided)
	g.CreatedAt, g.ExpiresAt = fromMs(created), fromMs(expires)
	return &g, nil
}

// CreateOAuthGrant stores a pending authorization request.
func (s *Store) CreateOAuthGrant(ctx context.Context, g *OAuthGrant) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO oauth_grants (id, client_id, redirect_uri, code_challenge, state, scope, resource, status, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		g.ID, g.ClientID, g.RedirectURI, g.CodeChallenge, nullStr(g.State), nullStr(g.Scope), nullStr(g.Resource), ms(g.CreatedAt), ms(g.ExpiresAt))
	return mapErr(err)
}

// GetOAuthGrant returns a grant by id.
func (s *Store) GetOAuthGrant(ctx context.Context, id string) (*OAuthGrant, error) {
	return scanGrant(s.r.QueryRowContext(ctx, `SELECT `+grantCols+` FROM oauth_grants WHERE id = ?`, id))
}

// GetOAuthGrantByCode returns the grant holding an authorization code.
func (s *Store) GetOAuthGrantByCode(ctx context.Context, codeHash []byte) (*OAuthGrant, error) {
	return scanGrant(s.r.QueryRowContext(ctx, `SELECT `+grantCols+` FROM oauth_grants WHERE code_hash = ?`, codeHash))
}

// DecideOAuthGrant moves a pending, unexpired grant to approved or denied.
func (s *Store) DecideOAuthGrant(ctx context.Context, id, status, adminID string, codeHash []byte, agentTokenID *string, codeExpiry, now time.Time) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE oauth_grants SET status = ?, decided_by = ?, code_hash = ?, agent_token_id = ?, expires_at = ?
		  WHERE id = ? AND status = 'pending' AND expires_at > ?`, status, adminID, codeHash, nullStr(agentTokenID), ms(codeExpiry), id, ms(now))
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}

// UseOAuthGrant consumes an approved code exactly once.
func (s *Store) UseOAuthGrant(ctx context.Context, id string, now time.Time) error {
	res, err := s.w.ExecContext(ctx, `UPDATE oauth_grants SET status = 'used' WHERE id = ? AND status = 'approved' AND expires_at > ?`, id, ms(now))
	if err != nil {
		return mapErr(err)
	}
	return mustAffect(res)
}

// SetAgentRefresh stores the hash of a token's current refresh token.
func (s *Store) SetAgentRefresh(ctx context.Context, tokenID string, refreshHash []byte, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE agent_tokens SET refresh_hash = ?, updated_at = ? WHERE id = ?`, refreshHash, ms(now), tokenID)
	return mapErr(err)
}

// GetAgentTokenByRefresh returns the token owning a refresh token.
func (s *Store) GetAgentTokenByRefresh(ctx context.Context, refreshHash []byte) (*AgentToken, error) {
	return scanAgentToken(s.r.QueryRowContext(ctx, `SELECT `+agentTokenCols+` FROM agent_tokens WHERE refresh_hash = ?`, refreshHash))
}

// PurgeOAuthGrants deletes grants that expired more than a day ago.
func (s *Store) PurgeOAuthGrants(ctx context.Context, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM oauth_grants WHERE expires_at < ?`, ms(now.Add(-24*time.Hour)))
	return mapErr(err)
}
