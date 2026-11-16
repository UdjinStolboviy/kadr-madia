package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// ───────────── middleware ─────────────

// requireSession loads the session from the cookie, slides its expiry and
// puts the admin into the request context. Missing/expired → 401.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cookieName)
		if err != nil || c.Value == "" {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		ctx := r.Context()
		now := s.now()
		sess, err := s.store.GetSession(ctx, auth.HashToken(c.Value), now)
		if errors.Is(err, db.ErrNotFound) {
			s.clearCookie(w)
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "session expired")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		a, err := s.store.GetAdmin(ctx, sess.AdminID)
		if errors.Is(err, db.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "account no longer exists")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if exp := auth.SessionExpiry(sess.CreatedAt, now); exp.Sub(sess.ExpiresAt) >= auth.SessionExtendEvery {
			if err := s.store.ExtendSession(ctx, sess.IDHash, exp); err != nil {
				s.log.WarnContext(ctx, "extend session failed", "err", err)
			} else {
				sess.ExpiresAt = exp
			}
		}
		ctx = context.WithValue(ctx, ctxAdmin, a)
		ctx = context.WithValue(ctx, ctxSession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isUnsafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// requireCSRF checks X-CSRF-Token on state-changing requests.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnsafe(r.Method) {
			sess := sessionFrom(r.Context())
			tok := r.Header.Get("X-CSRF-Token")
			if sess == nil || tok == "" || !auth.EqualConstantTime(tok, sess.CSRFToken) {
				httpx.WriteProblem(w, http.StatusForbidden, "csrf_failed", "missing or invalid X-CSRF-Token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireWriteRole blocks mutations for the read-only "viewer" role, except
// for the admin's own auth endpoints (logout, TOTP).
func (s *Server) requireWriteRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := adminFrom(r.Context())
		if a != nil && a.Role == "viewer" && isUnsafe(r.Method) && !strings.Contains(r.URL.Path, "/auth/") {
			httpx.WriteProblem(w, http.StatusForbidden, "forbidden", "viewer role is read-only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ───────────── cookies ─────────────

func (s *Server) setCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   !s.cfg.Dev,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !s.cfg.Dev, SameSite: http.SameSiteStrictMode,
	})
}

// ───────────── handlers ─────────────

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

type sessionResponse struct {
	Admin     adminDTO `json:"admin"`
	CSRFToken string   `json:"csrf_token"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	errs := map[string]string{}
	if req.Email == "" {
		errs["email"] = "required"
	}
	if req.Password == "" {
		errs["password"] = "required"
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}

	ip := httpx.ClientIP(r)
	if !s.ipLimit.Allow(ip) || !s.loginLimit.Allow(ip+"|"+req.Email) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteProblem(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts, try again in a minute")
		return
	}

	ctx := r.Context()
	a, err := s.store.GetAdminByEmail(ctx, req.Email)
	if errors.Is(err, db.ErrNotFound) {
		auth.DummyVerify(req.Password)
		s.auditLoginFailure(r, nil, req.Email, "unknown_email")
		httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(a.PasswordHash, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		s.auditLoginFailure(r, &a.ID, req.Email, "bad_password")
		httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if a.TOTPEnabled() {
		if strings.TrimSpace(req.TOTPCode) == "" {
			httpx.WriteProblem(w, http.StatusUnauthorized, "totp_required", "TOTP code required")
			return
		}
		secret, err := s.cfg.Keyring.Decrypt(a.TOTPSecretEnc, totpAAD(a.ID))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !auth.ValidateTOTP(req.TOTPCode, string(secret), s.now()) {
			s.auditLoginFailure(r, &a.ID, req.Email, "bad_totp")
			httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_credentials", "invalid TOTP code")
			return
		}
	}

	// Drop a pre-existing session presented with this request (fixation hygiene).
	if c, err := r.Cookie(s.cookieName); err == nil && c.Value != "" {
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
	sess := &db.Session{IDHash: hash, AdminID: a.ID, CSRFToken: csrf, IP: ip, UserAgent: truncate(r.UserAgent(), 512),
		CreatedAt: now, ExpiresAt: auth.SessionExpiry(now, now)}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateSession(ctx, sess); err != nil {
			return err
		}
		if err := tx.TouchAdminLogin(ctx, a.ID, now); err != nil {
			return err
		}
		r = r.WithContext(context.WithValue(ctx, ctxAdmin, a))
		return s.audit(r, tx, "auth.login", nil, &a.ID, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	a.LastLoginAt = &now
	s.setCookie(w, token, auth.SessionAbsolute)
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{Admin: toAdminDTO(a), CSRFToken: csrf})
}

func (s *Server) auditLoginFailure(r *http.Request, adminID *string, email, reason string) {
	ip := httpx.ClientIP(r)
	err := s.store.InsertAudit(r.Context(), &db.AuditEntry{
		ActorType: db.ActorAdmin, ActorID: adminID, Action: "auth.login_failed", Target: &email, IP: &ip, CreatedAt: s.now(),
	}, map[string]string{"reason": reason})
	if err != nil {
		s.log.WarnContext(r.Context(), "audit login failure", "err", err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	a := adminFrom(r.Context())
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.DeleteSession(r.Context(), sess.IDHash); err != nil {
			return err
		}
		return s.audit(r, tx, "auth.logout", nil, &a.ID, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{
		Admin: toAdminDTO(adminFrom(r.Context())), CSRFToken: sessionFrom(r.Context()).CSRFToken,
	})
}

func totpAAD(adminID string) []byte { return []byte("admin_totp:" + adminID) }

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	a := adminFrom(r.Context())
	if a.TOTPEnabled() {
		httpx.WriteProblem(w, http.StatusConflict, "totp_already_enabled", "disable TOTP before setting up a new secret")
		return
	}
	secret, url, err := auth.GenerateTOTP(a.Email)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	enc, err := s.cfg.Keyring.Encrypt([]byte(secret), totpAAD(a.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Stored as pending (totp_enabled_at stays NULL) until /totp/enable.
	if err := s.store.SetAdminTOTP(r.Context(), a.ID, enc, nil); err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_url": url})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	a := adminFrom(r.Context())
	if a.TOTPEnabled() {
		httpx.WriteProblem(w, http.StatusConflict, "totp_already_enabled", "TOTP is already enabled")
		return
	}
	if len(a.TOTPSecretEnc) == 0 {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_code", "call /auth/totp/setup first")
		return
	}
	secret, err := s.cfg.Keyring.Decrypt(a.TOTPSecretEnc, totpAAD(a.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !auth.ValidateTOTP(req.Code, string(secret), s.now()) {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_code", "invalid TOTP code")
		return
	}
	now := s.now()
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SetAdminTOTP(r.Context(), a.ID, a.TOTPSecretEnc, &now); err != nil {
			return err
		}
		return s.audit(r, tx, "auth.totp_enable", nil, &a.ID, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	a := adminFrom(r.Context())
	ok, err := auth.VerifyPassword(a.PasswordHash, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_credentials", "invalid password")
		return
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SetAdminTOTP(r.Context(), a.ID, nil, nil); err != nil {
			return err
		}
		return s.audit(r, tx, "auth.totp_disable", nil, &a.ID, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
