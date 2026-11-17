package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

const (
	webauthnCeremonyTTL = 5 * time.Minute
	maxPasskeysPerAdmin = 20
)

// webAuthnState holds the relying party and the short-lived ceremony
// sessions (challenges). Single process, in memory; a ceremony is consumed
// by its first finish attempt, so a replayed response never validates.
type webAuthnState struct {
	wa  *webauthn.WebAuthn // nil → disabled
	err error

	mu         sync.Mutex
	ceremonies map[string]ceremony
}

type ceremony struct {
	kind    string // "register" | "login"
	adminID string // register only
	data    webauthn.SessionData
	expires time.Time
}

func newWebAuthnState(cfg Config) *webAuthnState {
	st := &webAuthnState{ceremonies: map[string]ceremony{}}
	if cfg.WebAuthnRPID == "" || len(cfg.WebAuthnOrigins) == 0 {
		return st
	}
	st.wa, st.err = webauthn.New(&webauthn.Config{
		RPDisplayName: "Kadr",
		RPID:          cfg.WebAuthnRPID,
		RPOrigins:     cfg.WebAuthnOrigins,
	})
	if st.err != nil {
		st.wa = nil
	}
	return st
}

func (st *webAuthnState) put(kind, adminID string, data *webauthn.SessionData, now time.Time) (string, error) {
	id, err := auth.NewCSRFToken() // 256-bit random, url-safe
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, c := range st.ceremonies {
		if now.After(c.expires) {
			delete(st.ceremonies, k)
		}
	}
	if len(st.ceremonies) > 10000 {
		return "", errors.New("too many pending webauthn ceremonies")
	}
	st.ceremonies[id] = ceremony{kind: kind, adminID: adminID, data: *data, expires: now.Add(webauthnCeremonyTTL)}
	return id, nil
}

// take removes and returns a ceremony (single use).
func (st *webAuthnState) take(id, kind, adminID string, now time.Time) (*webauthn.SessionData, bool) {
	st.mu.Lock()
	c, ok := st.ceremonies[id]
	delete(st.ceremonies, id)
	st.mu.Unlock()
	if !ok || c.kind != kind || c.adminID != adminID || now.After(c.expires) {
		return nil, false
	}
	return &c.data, true
}

// waUser adapts an admin and their credentials to webauthn.User.
type waUser struct {
	admin *db.Admin
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(u.admin.ID) }
func (u *waUser) WebAuthnName() string                       { return u.admin.Email }
func (u *waUser) WebAuthnDisplayName() string                { return u.admin.Email }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func toWACredential(c *db.WebAuthnCredential) webauthn.Credential {
	var tr []protocol.AuthenticatorTransport
	for _, t := range strings.Split(c.Transports, ",") {
		if t != "" {
			tr = append(tr, protocol.AuthenticatorTransport(t))
		}
	}
	return webauthn.Credential{
		ID: c.CredentialID, PublicKey: c.PublicKey, Transport: tr,
		Flags:         webauthn.CredentialFlagsFromMsgpByte(c.Flags),
		Authenticator: webauthn.Authenticator{AAGUID: c.AAGUID, SignCount: c.SignCount},
	}
}

func (s *Server) loadWAUser(ctx context.Context, a *db.Admin) (*waUser, error) {
	rows, err := s.store.ListWebAuthnCredentials(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	u := &waUser{admin: a}
	for _, c := range rows {
		u.creds = append(u.creds, toWACredential(c))
	}
	return u, nil
}

func (s *Server) webAuthnEnabled(w http.ResponseWriter) bool {
	if s.wa.wa == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "webauthn_disabled",
			"WebAuthn is not configured (set KADR_WEBAUTHN_RP_ID and KADR_WEBAUTHN_ORIGINS)")
		return false
	}
	return true
}

type passkeyDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Transports []string   `json:"transports"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func toPasskeyDTO(c *db.WebAuthnCredential) passkeyDTO {
	tr := []string{}
	for _, t := range strings.Split(c.Transports, ",") {
		if t != "" {
			tr = append(tr, t)
		}
	}
	return passkeyDTO{ID: c.ID, Name: c.Name, Transports: tr, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt}
}

// ───────────── registration ─────────────

func (s *Server) handleWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !s.webAuthnEnabled(w) {
		return
	}
	a := adminFrom(r.Context())
	u, err := s.loadWAUser(r.Context(), a)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(u.creds) >= maxPasskeysPerAdmin {
		httpx.WriteProblem(w, http.StatusConflict, "too_many_credentials", "passkey limit reached; delete one first")
		return
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, session, err := s.wa.wa.BeginRegistration(u,
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	id, err := s.wa.put("register", a.ID, session, s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": creation})
}

type webAuthnFinishRequest struct {
	CeremonyID string          `json:"ceremony_id"`
	Name       string          `json:"name"`
	Credential json.RawMessage `json:"credential"`
}

func (s *Server) handleWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !s.webAuthnEnabled(w) {
		return
	}
	var req webAuthnFinishRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 80 {
		validationFailed(w, map[string]string{"name": "required, at most 80 characters"})
		return
	}
	ctx := r.Context()
	a := adminFrom(ctx)
	session, ok := s.wa.take(req.CeremonyID, "register", a.ID, s.now())
	if !ok {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_ceremony", "registration expired or already used; start again")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_credential", "invalid registration response")
		return
	}
	u, err := s.loadWAUser(ctx, a)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cred, err := s.wa.wa.CreateCredential(u, *session, parsed)
	if err != nil {
		s.log.InfoContext(ctx, "webauthn registration rejected", "err", err)
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_credential", "registration could not be verified")
		return
	}
	var tr []string
	for _, t := range cred.Transport {
		tr = append(tr, string(t))
	}
	row := &db.WebAuthnCredential{
		ID: db.NewID(db.PrefixWebAuthn), AdminID: a.ID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: cred.Authenticator.SignCount, Transports: strings.Join(tr, ","), AAGUID: cred.Authenticator.AAGUID,
		Flags: cred.Flags.MsgpByte(), Name: req.Name, CreatedAt: s.now(),
	}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateWebAuthnCredential(ctx, row); err != nil {
			return err
		}
		return s.audit(r, tx, "auth.webauthn_register", nil, &row.ID, map[string]string{"name": row.Name})
	})
	if errors.Is(err, db.ErrConflict) {
		httpx.WriteProblem(w, http.StatusConflict, "credential_exists", "this authenticator is already registered")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPasskeyDTO(row))
}

func (s *Server) handleWebAuthnList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListWebAuthnCredentials(r.Context(), adminFrom(r.Context()).ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]passkeyDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, toPasskeyDTO(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) handleWebAuthnDelete(w http.ResponseWriter, r *http.Request) {
	a := adminFrom(r.Context())
	id := chi.URLParam(r, "id")
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.DeleteWebAuthnCredential(r.Context(), a.ID, id); err != nil {
			return err
		}
		return s.audit(r, tx, "auth.webauthn_delete", nil, &id, nil)
	})
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "passkey")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ───────────── passkey login ─────────────

// passkeyRateLimited applies the login limiters: begin only counts against the
// per-IP limiter, finish also against the stricter per-IP passkey bucket.
func (s *Server) passkeyRateLimited(w http.ResponseWriter, r *http.Request, finish bool) bool {
	ip := httpx.ClientIP(r)
	if !s.ipLimit.Allow(ip) || (finish && !s.loginLimit.Allow(ip+"|passkey")) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteProblem(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts, try again in a minute")
		return true
	}
	return false
}

// handleWebAuthnLoginBegin starts a usernameless (discoverable credential)
// ceremony. It takes no identifier, so it cannot reveal whether an account
// exists.
func (s *Server) handleWebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.webAuthnEnabled(w) || s.passkeyRateLimited(w, r, false) {
		return
	}
	assertion, session, err := s.wa.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	id, err := s.wa.put("login", "", session, s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": assertion})
}

func (s *Server) handleWebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.webAuthnEnabled(w) || s.passkeyRateLimited(w, r, true) {
		return
	}
	var req webAuthnFinishRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	ctx := r.Context()
	fail := func(adminID *string, reason string) {
		s.auditLoginFailure(r, adminID, "passkey", reason)
		httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_credentials", "passkey sign-in failed")
	}
	session, ok := s.wa.take(req.CeremonyID, "login", "", s.now())
	if !ok {
		fail(nil, "bad_ceremony")
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		fail(nil, "malformed")
		return
	}
	var found *db.Admin
	var stored *db.WebAuthnCredential
	user, cred, err := s.wa.wa.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		c, err := s.store.GetWebAuthnCredentialByCredID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		if c.AdminID != string(userHandle) {
			return nil, errors.New("user handle mismatch")
		}
		a, err := s.store.GetAdmin(ctx, c.AdminID)
		if err != nil {
			return nil, err
		}
		u, err := s.loadWAUser(ctx, a)
		if err != nil {
			return nil, err
		}
		found, stored = a, c
		return u, nil
	}, *session, parsed)
	if err != nil || user == nil || found == nil {
		var id *string
		if found != nil {
			id = &found.ID
		}
		fail(id, "verification_failed")
		return
	}
	if cred.Authenticator.CloneWarning {
		// Sign-count regression: possible cloned authenticator.
		fail(&found.ID, "sign_count_regression")
		return
	}
	if !cred.Flags.UserVerified && !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		fail(&found.ID, "no_user_verification")
		return
	}

	// Drop a pre-existing session presented with this request (fixation hygiene).
	if c, err := s.sessionCookie(r); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(ctx, auth.HashToken(c.Value))
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	csrf, err := auth.NewCSRFToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := s.now()
	ip := httpx.ClientIP(r)
	sess := &db.Session{IDHash: hash, AdminID: found.ID, CSRFToken: csrf, IP: ip, UserAgent: truncate(r.UserAgent(), 512),
		CreatedAt: now, ExpiresAt: auth.SessionExpiry(now, now)}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateSession(ctx, sess); err != nil {
			return err
		}
		if err := tx.TouchAdminLogin(ctx, found.ID, now); err != nil {
			return err
		}
		if err := tx.TouchWebAuthnCredential(ctx, stored.ID, cred.Authenticator.SignCount, cred.Flags.MsgpByte(), now); err != nil {
			return err
		}
		rr := r.WithContext(context.WithValue(ctx, ctxAdmin, found))
		return s.audit(rr, tx, "auth.login", nil, &found.ID, map[string]string{"method": "passkey", "credential": stored.ID})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	found.LastLoginAt = &now
	s.setCookie(w, r, token, auth.SessionAbsolute)
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{Admin: toAdminDTO(found), CSRFToken: csrf})
}
