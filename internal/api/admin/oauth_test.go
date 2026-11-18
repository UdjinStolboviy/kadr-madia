package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func formPost(e *env, path string, form url.Values) resp {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.admin.ServeHTTP(rec, req)
	return resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.Bytes()}
}

func TestOAuthFlow(t *testing.T) {
	ae := newAgentEnv(t)
	anon := &client{e: ae.env}

	// Discovery: the MCP 401 points at the resource metadata.
	r := (&agentClient{e: ae.env}).rpcRaw(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, nil)
	if r.code != 401 || !strings.Contains(r.hdr.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("401 header: %v", r.hdr)
	}
	r = anon.do("GET", "/.well-known/oauth-protected-resource/mcp", nil, false)
	if r.code != 200 || !strings.HasSuffix(r.json(t)["resource"].(string), "/mcp") {
		t.Fatalf("resource metadata: %d %s", r.code, r.body)
	}
	r = anon.do("GET", "/.well-known/oauth-authorization-server", nil, false)
	md := r.json(t)
	if md["code_challenge_methods_supported"].([]any)[0] != "S256" || !strings.HasSuffix(md["registration_endpoint"].(string), "/oauth/register") {
		t.Fatalf("AS metadata: %s", r.body)
	}

	// Registration.
	expectBad := anon.do("POST", "/oauth/register", map[string]any{"client_name": "x", "redirect_uris": []string{"http://evil.example/cb"}}, false)
	if expectBad.code != 400 {
		t.Fatalf("http redirect accepted: %d", expectBad.code)
	}
	cb := "https://claude.ai/api/mcp/auth_callback"
	r = anon.do("POST", "/oauth/register", map[string]any{"client_name": "Claude", "redirect_uris": []string{cb}, "token_endpoint_auth_method": "none"}, false)
	if r.code != 201 {
		t.Fatalf("register: %d %s", r.code, r.body)
	}
	clientID := r.json(t)["client_id"].(string)

	// Authorization request → consent screen.
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {cb}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"xyz"}, "scope": {"kadr:edit"}}
	r = anon.do("GET", "/oauth/authorize?"+q.Encode(), nil, false)
	if r.code != http.StatusFound || !strings.HasPrefix(r.hdr.Get("Location"), "/oauth/consent?grant=") {
		t.Fatalf("authorize: %d %v", r.code, r.hdr)
	}
	grant := strings.TrimPrefix(r.hdr.Get("Location"), "/oauth/consent?grant=")
	// Unknown redirect URIs never redirect.
	bad := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://evil.example/cb"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if r := anon.do("GET", "/oauth/authorize?"+bad.Encode(), nil, false); r.code != 400 {
		t.Fatalf("foreign redirect: %d", r.code)
	}

	// The consent API needs a signed-in admin.
	expectProblem(t, anon.do("GET", "/admin/api/oauth/grants/"+grant, nil, false), 401, "unauthenticated")
	r = ae.admin.do("GET", "/admin/api/oauth/grants/"+grant, nil, false)
	if r.json(t)["client_name"] != "Claude" || r.json(t)["suggested_level"] != "edit" {
		t.Fatalf("grant: %s", r.body)
	}
	r = ae.admin.do("POST", "/admin/api/oauth/grants/"+grant+"/approve", map[string]any{"level": "edit", "project_ids": []string{ae.pid}, "approval_mode": "destructive"}, true)
	if r.code != 200 {
		t.Fatalf("approve: %d %s", r.code, r.body)
	}
	ru, _ := url.Parse(r.json(t)["redirect_url"].(string))
	code := ru.Query().Get("code")
	if ru.Query().Get("state") != "xyz" || code == "" {
		t.Fatalf("redirect: %s", ru)
	}
	expectProblem(t, ae.admin.do("POST", "/admin/api/oauth/grants/"+grant+"/approve", map[string]any{"level": "read"}, true), 409, "not_pending")

	// Token exchange: PKCE is enforced; codes are single-use.
	tf := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {cb}, "code_verifier": {"wrong" + verifier}}
	if r := formPost(ae.env, "/oauth/token", tf); r.code != 400 {
		t.Fatalf("bad verifier: %d", r.code)
	}
	tf.Set("code_verifier", verifier)
	// The failed attempt above did not consume the code.
	r = formPost(ae.env, "/oauth/token", tf)
	if r.code != 200 {
		t.Fatalf("token: %d %s", r.code, r.body)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	json.Unmarshal(r.body, &tok)
	if !strings.HasPrefix(tok.AccessToken, "kd_agent_") || tok.Scope != "kadr:edit" {
		t.Fatalf("tokens: %s", r.body)
	}
	if r := formPost(ae.env, "/oauth/token", tf); r.code != 400 {
		t.Fatalf("code reuse: %d", r.code)
	}

	// The access token is a scoped agent token.
	ag := &agentClient{e: ae.env, token: tok.AccessToken}
	if r := ag.do("GET", "/admin/api/projects", nil, ""); r.code != 200 || len(r.json(t)["items"].([]any)) != 1 {
		t.Fatalf("oauth agent: %d %s", r.code, r.body)
	}

	// Refresh rotates both tokens.
	rf := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}, "client_id": {clientID}}
	r = formPost(ae.env, "/oauth/token", rf)
	if r.code != 200 {
		t.Fatalf("refresh: %d %s", r.code, r.body)
	}
	var tok2 struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(r.body, &tok2)
	expectProblem(t, ag.do("GET", "/admin/api/projects", nil, ""), 401, "unauthenticated")
	if r := formPost(ae.env, "/oauth/token", rf); r.code != 400 {
		t.Fatalf("old refresh token reused: %d", r.code)
	}
	ag.token = tok2.AccessToken
	if r := ag.do("GET", "/admin/api/agent/me", nil, ""); r.code != 200 {
		t.Fatalf("new access token: %d", r.code)
	}

	// Revoking the agent kills refresh too.
	r = ae.admin.do("GET", "/admin/api/agents", nil, false)
	var agents struct {
		Items []struct {
			ID            string  `json:"id"`
			OAuthClientID *string `json:"oauth_client_id"`
		} `json:"items"`
	}
	json.Unmarshal(r.body, &agents)
	if len(agents.Items) != 1 || agents.Items[0].OAuthClientID == nil {
		t.Fatalf("agent list: %s", r.body)
	}
	ae.admin.do("DELETE", "/admin/api/agents/"+agents.Items[0].ID, nil, true)
	rf.Set("refresh_token", tok2.RefreshToken)
	if r := formPost(ae.env, "/oauth/token", rf); r.code != 400 {
		t.Fatalf("refresh after revoke: %d", r.code)
	}

	// Denial redirects with access_denied.
	r = anon.do("GET", "/oauth/authorize?"+q.Encode(), nil, false)
	grant2 := strings.TrimPrefix(r.hdr.Get("Location"), "/oauth/consent?grant=")
	r = ae.admin.do("POST", "/admin/api/oauth/grants/"+grant2+"/deny", nil, true)
	if !strings.Contains(r.json(t)["redirect_url"].(string), "error=access_denied") {
		t.Fatalf("deny: %s", r.body)
	}
}
