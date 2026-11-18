package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/access"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// OAuth 2.1 authorization server for MCP clients (MCP authorization spec):
//
//   - RFC 9728 protected resource metadata  /.well-known/oauth-protected-resource[/mcp]
//   - RFC 8414 authorization server metadata /.well-known/oauth-authorization-server
//   - RFC 7591 dynamic client registration  POST /oauth/register (public clients only)
//   - authorization code + PKCE (S256)       GET /oauth/authorize → human consent in the admin → redirect
//   - token endpoint                         POST /oauth/token (authorization_code, refresh_token)
//
// The access token is an ordinary agent token (kd_agent_…): every permission
// rule, approval and audit entry applies unchanged. The human who approves
// the consent screen chooses its level, projects and approval mode.

const (
	oauthGrantTTL    = 10 * time.Minute
	oauthCodeTTL     = 2 * time.Minute
	maxRedirectURIs  = 5
	maxClientNameLen = 100
)

var oauthScopes = []string{"kadr:read", "kadr:edit", "kadr:full"}

func (s *Server) issuer(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) oauthRoutes(r chi.Router) {
	r.Get("/.well-known/oauth-protected-resource", s.handleResourceMetadata)
	r.Get("/.well-known/oauth-protected-resource/*", s.handleResourceMetadata)
	r.Get("/.well-known/oauth-authorization-server", s.handleASMetadata)
	r.Get("/.well-known/oauth-authorization-server/*", s.handleASMetadata)
	r.Post("/oauth/register", s.handleOAuthRegister)
	r.Get("/oauth/authorize", s.handleOAuthAuthorize)
	r.Post("/oauth/token", s.handleOAuthToken)
}

func (s *Server) handleResourceMetadata(w http.ResponseWriter, r *http.Request) {
	iss := s.issuer(r)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"resource":                 iss + "/mcp",
		"authorization_servers":    []string{iss},
		"scopes_supported":         oauthScopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Kadr",
		"resource_documentation":   "https://github.com/UdjinStolboviy/kadr-madia/blob/main/docs/AGENTS.md",
	})
}

func (s *Server) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	iss := s.issuer(r)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/oauth/authorize",
		"token_endpoint":                        iss + "/oauth/token",
		"registration_endpoint":                 iss + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      oauthScopes,
	})
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// validRedirectURI accepts https URLs and http loopback URLs (native apps).
func validRedirectURI(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Fragment != "" || p.User != nil || p.Host == "" {
		return false
	}
	switch p.Scheme {
	case "https":
		return true
	case "http":
		h := p.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// handleOAuthRegister: RFC 7591, public clients only.
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	if !s.ipLimit.Allow("oauth-register:" + httpx.ClientIP(r)) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many registrations")
		return
	}
	var in struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > maxRedirectURIs {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "1 to 5 redirect_uris are required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be https, or http on localhost: "+u)
			return
		}
	}
	if in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) are supported")
		return
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len([]rune(name)) > maxClientNameLen {
		name = string([]rune(name)[:maxClientNameLen])
	}
	ip := httpx.ClientIP(r)
	c := &db.OAuthClient{ID: db.NewID(db.PrefixOAuthClient), Name: name, RedirectURIs: in.RedirectURIs, CreatedIP: &ip, CreatedAt: s.now()}
	if err := s.store.CreateOAuthClient(r.Context(), c); err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"client_id": c.ID, "client_id_issued_at": c.CreatedAt.Unix(), "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}

// authorizeErrorPage is shown when the client or redirect URI is invalid
// (the spec forbids redirecting to an unverified URI).
func authorizeErrorPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Kadr</title><body style="font-family:system-ui;margin:3rem">
<h1>Cannot connect this application</h1><p>%s</p></body>`, html.EscapeString(msg))
}

func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", "kadr")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleOAuthAuthorize validates the request and sends the browser to the
// consent screen of the admin UI (which requires an admin sign-in).
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.store.GetOAuthClient(r.Context(), q.Get("client_id"))
	if errors.Is(err, db.ErrNotFound) {
		authorizeErrorPage(w, "Unknown client_id. The application must register first.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	if !slices.Contains(c.RedirectURIs, redirect) {
		authorizeErrorPage(w, "The redirect_uri is not registered for this application.")
		return
	}
	state := q.Get("state")
	if q.Get("response_type") != "code" {
		redirectWithError(w, r, redirect, state, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	challenge := q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || len(challenge) < 43 || len(challenge) > 128 {
		redirectWithError(w, r, redirect, state, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	if res := q.Get("resource"); res != "" && !strings.HasPrefix(res, s.issuer(r)) {
		redirectWithError(w, r, redirect, state, "invalid_target", "unknown resource")
		return
	}
	now := s.now()
	g := &db.OAuthGrant{ID: db.NewID(db.PrefixOAuthGrant), ClientID: c.ID, RedirectURI: redirect, CodeChallenge: challenge,
		CreatedAt: now, ExpiresAt: now.Add(oauthGrantTTL)}
	if state != "" {
		g.State = &state
	}
	if sc := q.Get("scope"); sc != "" {
		g.Scope = &sc
	}
	if res := q.Get("resource"); res != "" {
		g.Resource = &res
	}
	if err := s.store.CreateOAuthGrant(r.Context(), g); err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.store.PurgeOAuthGrants(r.Context(), now)
	http.Redirect(w, r, "/oauth/consent?grant="+url.QueryEscape(g.ID), http.StatusFound)
}

// requestedLevel maps OAuth scopes to the suggested access level.
func requestedLevel(scope *string) string {
	if scope == nil {
		return access.LevelRead
	}
	switch {
	case strings.Contains(*scope, "kadr:full"):
		return access.LevelFull
	case strings.Contains(*scope, "kadr:edit"):
		return access.LevelEdit
	}
	return access.LevelRead
}

func (s *Server) loadGrant(w http.ResponseWriter, r *http.Request) (*db.OAuthGrant, *db.OAuthClient, bool) {
	g, err := s.store.GetOAuthGrant(r.Context(), chi.URLParam(r, "grant_id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "authorization request")
		return nil, nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	if g.Status != "pending" || !s.now().Before(g.ExpiresAt) {
		httpx.WriteProblem(w, http.StatusConflict, "not_pending", "this authorization request was already answered or has expired; start again from the application")
		return nil, nil, false
	}
	c, err := s.store.GetOAuthClient(r.Context(), g.ClientID)
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	return g, c, true
}

// handleGetGrant: GET /admin/api/oauth/grants/{grant_id} — data for the consent screen.
func (s *Server) handleGetGrant(w http.ResponseWriter, r *http.Request) {
	g, c, ok := s.loadGrant(w, r)
	if !ok {
		return
	}
	u, _ := url.Parse(g.RedirectURI)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": g.ID, "client_id": c.ID, "client_name": c.Name, "redirect_host": u.Host, "redirect_uri": g.RedirectURI,
		"scope": g.Scope, "suggested_level": requestedLevel(g.Scope), "client_registered_at": ts(c.CreatedAt),
		"expires_at": ts(g.ExpiresAt),
	})
}

// handleApproveGrant: POST /admin/api/oauth/grants/{grant_id}/approve — the
// body is the same as POST /agents (level, projects, approval mode, …). It
// creates the agent token and returns the client's redirect URL with the code.
func (s *Server) handleApproveGrant(w http.ResponseWriter, r *http.Request) {
	g, c, ok := s.loadGrant(w, r)
	if !ok {
		return
	}
	in, err := decodeAgentInput(w, r)
	if err != nil {
		badJSON(w, err)
		return
	}
	if in.Name == nil {
		n := c.Name
		in.Name = &n
	}
	if !in.allProjects && in.ProjectIDs == nil {
		in.allProjects = true
	}
	now := s.now()
	t := &db.AgentToken{ID: db.NewID(db.PrefixAgentToken), CreatedBy: adminFrom(r.Context()).ID, OAuthClientID: &c.ID, CreatedAt: now, UpdatedAt: now}
	if errs := s.applyAgentInput(r, in, t, true); len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	// The real secret is minted at the code exchange; until then the token
	// carries an unusable random one.
	placeholder, err := auth.GenerateAgentToken(s.cfg.Pepper)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	t.Prefix, t.SecretHash = placeholder.Prefix, sha256Sum(randomToken())
	code := randomToken()
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.CreateAgentToken(r.Context(), t); err != nil {
			return err
		}
		if err := tx.DecideOAuthGrant(r.Context(), g.ID, "approved", adminFrom(r.Context()).ID, sha256Sum(code), &t.ID, now.Add(oauthCodeTTL), now); err != nil {
			return err
		}
		return s.audit(r, tx, "agent.oauth.approve", nil, &t.ID, map[string]any{"client_id": c.ID, "client_name": c.Name,
			"redirect_uri": g.RedirectURI, "level": t.Level, "project_ids": t.ProjectIDs, "approval_mode": t.ApprovalMode})
	})
	if errors.Is(err, db.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusConflict, "not_pending", "this authorization request was already answered or has expired")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, _ := url.Parse(g.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if g.State != nil {
		q.Set("state", *g.State)
	}
	q.Set("iss", s.issuer(r))
	u.RawQuery = q.Encode()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"redirect_url": u.String(), "agent": s.toAgentDTO(t)})
}

// handleDenyGrant: POST /admin/api/oauth/grants/{grant_id}/deny.
func (s *Server) handleDenyGrant(w http.ResponseWriter, r *http.Request) {
	g, c, ok := s.loadGrant(w, r)
	if !ok {
		return
	}
	now := s.now()
	if err := s.store.DecideOAuthGrant(r.Context(), g.ID, "denied", adminFrom(r.Context()).ID, nil, nil, now, now); err != nil &&
		!errors.Is(err, db.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "agent.oauth.deny", nil, &g.ID, map[string]any{"client_id": c.ID, "client_name": c.Name})
	u, _ := url.Parse(g.RedirectURI)
	q := u.Query()
	q.Set("error", "access_denied")
	q.Set("error_description", "an administrator declined the request")
	if g.State != nil {
		q.Set("state", *g.State)
	}
	u.RawQuery = q.Encode()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"redirect_url": u.String()})
}

// handleOAuthToken: POST /oauth/token (application/x-www-form-urlencoded).
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if !s.ipLimit.Allow("oauth-token:" + httpx.ClientIP(r)) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many token requests")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "form body required")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	ctx := r.Context()
	g, err := s.store.GetOAuthGrantByCode(ctx, sha256Sum(f.Get("code")))
	now := s.now()
	if err != nil || g.Status != "approved" || !now.Before(g.ExpiresAt) || g.AgentTokenID == nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is invalid, expired or already used")
		return
	}
	if f.Get("client_id") != g.ClientID || (f.Get("redirect_uri") != "" && f.Get("redirect_uri") != g.RedirectURI) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri does not match the authorization request")
		return
	}
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != g.CodeChallenge {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	if err := s.store.UseOAuthGrant(ctx, g.ID, now); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was already used")
		return
	}
	s.issueTokens(w, r, *g.AgentTokenID, g.ClientID, "agent.oauth.token")
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	t, err := s.store.GetAgentTokenByRefresh(r.Context(), sha256Sum(f.Get("refresh_token")))
	if err != nil || t.OAuthClientID == nil || *t.OAuthClientID != f.Get("client_id") || !t.Active(s.now()) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is invalid, revoked or expired")
		return
	}
	s.issueTokens(w, r, t.ID, *t.OAuthClientID, "agent.oauth.refresh")
}

// issueTokens rotates the agent token's secret and refresh token.
func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, tokenID, clientID, action string) {
	ctx := r.Context()
	gen, err := auth.GenerateAgentToken(s.cfg.Pepper)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	refresh := "kd_refresh_" + randomToken()
	now := s.now()
	var t *db.AgentToken
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		var err error
		if t, err = tx.GetAgentToken(ctx, tokenID); err != nil {
			return err
		}
		if !t.Active(now) {
			return db.ErrNotFound
		}
		if err := tx.RotateAgentToken(ctx, t.ID, gen.Prefix, gen.Hash, now); err != nil {
			return err
		}
		if err := tx.SetAgentRefresh(ctx, t.ID, sha256Sum(refresh), now); err != nil {
			return err
		}
		if err := tx.TouchOAuthClient(ctx, clientID, now); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &db.AuditEntry{ActorType: db.ActorAgent, ActorID: &t.ID, Action: action, Target: &clientID,
			IP: ptrTo(httpx.ClientIP(r)), CreatedAt: now}, map[string]any{"prefix": gen.Prefix})
	})
	if errors.Is(err, db.ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the agent token was revoked or has expired")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp := map[string]any{"access_token": gen.Secret, "token_type": "Bearer", "refresh_token": refresh,
		"scope": "kadr:" + t.Level}
	if t.ExpiresAt != nil {
		resp["expires_in"] = int64(t.ExpiresAt.Sub(now).Seconds())
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
