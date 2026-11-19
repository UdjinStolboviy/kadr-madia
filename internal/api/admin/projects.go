package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

const (
	maxNameLen    = 100
	maxOrigins    = 50
	maxAPIKeyName = 100
)

// ───────────── projects ─────────────

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProjects(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]projectDTO, 0, len(ps))
	t := agentFrom(r.Context())
	for _, p := range ps {
		if t != nil && !t.CoversProject(p.ID) {
			continue
		}
		out = append(out, toProjectDTO(p))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[projectDTO]{Items: out})
}

type createProjectRequest struct {
	Slug             string   `json:"slug"`
	Name             string   `json:"name"`
	Environment      string   `json:"environment"`
	StorageProfileID *string  `json:"storage_profile_id"`
	QuotaBytes       *int64   `json:"quota_bytes"`
	AllowedOrigins   []string `json:"allowed_origins"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	ctx := r.Context()
	errs := map[string]string{}
	req.Slug = strings.TrimSpace(req.Slug)
	if !slugRe.MatchString(req.Slug) {
		errs["slug"] = "must match ^[a-z][a-z0-9-]{1,31}$"
	}
	name, msg := validName(req.Name)
	if msg != "" {
		errs["name"] = msg
	}
	if req.Environment == "" {
		req.Environment = "live"
	}
	if req.Environment != "live" && req.Environment != "test" {
		errs["environment"] = `must be "live" or "test"`
	}
	if req.StorageProfileID != nil && *req.StorageProfileID == "" {
		req.StorageProfileID = nil
	}
	if req.StorageProfileID != nil {
		if m := s.checkProfileExists(r, *req.StorageProfileID); m != "" {
			errs["storage_profile_id"] = m
		}
	}
	if req.QuotaBytes != nil && *req.QuotaBytes < 0 {
		errs["quota_bytes"] = "must be >= 0 or null"
	}
	origins, msg := normalizeOrigins(req.AllowedOrigins)
	if msg != "" {
		errs["allowed_origins"] = msg
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}

	now := s.now()
	p := &db.Project{ID: db.NewID(db.PrefixProject), Slug: req.Slug, Name: name, Environment: req.Environment,
		StorageProfileID: req.StorageProfileID, AllowedOrigins: origins, QuotaBytes: req.QuotaBytes,
		CreatedAt: now, UpdatedAt: now}
	adminID := actorID(ctx)
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateProject(ctx, p); err != nil {
			return err
		}
		for _, dp := range preset.DefaultPresets() {
			if err := tx.CreatePreset(ctx, &db.Preset{ID: db.NewID(db.PrefixPreset), ProjectID: p.ID, Slug: dp.Slug,
				Kind: dp.Kind, Description: dp.Description, CurrentVersion: 1, Params: dp.Params, CreatedAt: now}, adminID); err != nil {
				return err
			}
		}
		return s.audit(r, tx, "project.create", &p.ID, &p.ID, map[string]any{"slug": p.Slug, "environment": p.Environment})
	})
	if errors.Is(err, db.ErrConflict) {
		httpx.WriteProblem(w, http.StatusConflict, "slug_taken", fmt.Sprintf("slug %q is already in use", p.Slug))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toProjectDTO(p))
}

func (s *Server) loadProject(w http.ResponseWriter, r *http.Request) (*db.Project, bool) {
	p, err := s.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "project")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return p, true
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.loadProject(w, r); ok {
		httpx.WriteJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// handlePatchProject applies a partial update. Fields absent from the body
// are left unchanged; explicit null clears storage_profile_id / quota_bytes.
func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		badJSON(w, err)
		return
	}
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	errs := map[string]string{}
	changed := []string{}
	isNull := func(raw json.RawMessage) bool { return string(raw) == "null" }

	if raw, ok := body["name"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			errs["name"] = "must be a string"
		} else if name, msg := validName(v); msg != "" {
			errs["name"] = msg
		} else {
			p.Name = name
			changed = append(changed, "name")
		}
	}
	if raw, ok := body["storage_profile_id"]; ok {
		var v string
		if isNull(raw) {
			p.StorageProfileID = nil
			changed = append(changed, "storage_profile_id")
		} else if err := json.Unmarshal(raw, &v); err != nil {
			errs["storage_profile_id"] = "must be a string or null"
		} else if v == "" {
			p.StorageProfileID = nil
			changed = append(changed, "storage_profile_id")
		} else if msg := s.checkProfileExists(r, v); msg != "" {
			errs["storage_profile_id"] = msg
		} else {
			p.StorageProfileID = &v
			changed = append(changed, "storage_profile_id")
		}
	}
	if raw, ok := body["quota_bytes"]; ok {
		var v int64
		if isNull(raw) {
			p.QuotaBytes = nil
			changed = append(changed, "quota_bytes")
		} else if err := json.Unmarshal(raw, &v); err != nil || v < 0 {
			errs["quota_bytes"] = "must be >= 0 or null"
		} else {
			p.QuotaBytes = &v
			changed = append(changed, "quota_bytes")
		}
	}
	if raw, ok := body["allowed_origins"]; ok {
		var v []string
		if err := json.Unmarshal(raw, &v); err != nil {
			errs["allowed_origins"] = "must be an array of strings"
		} else if origins, msg := normalizeOrigins(v); msg != "" {
			errs["allowed_origins"] = msg
		} else {
			p.AllowedOrigins = origins
			changed = append(changed, "allowed_origins")
		}
	}
	if raw, ok := body["default_mode"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil || !preset.ValidMode(v) {
			errs["default_mode"] = "must be compact, balanced or quality"
		} else if v != p.DefaultMode {
			p.DefaultMode = v
			changed = append(changed, "default_mode")
		}
	}
	if raw, ok := body["disabled"]; ok {
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			errs["disabled"] = "must be a boolean"
		} else if v && p.DisabledAt == nil {
			now := s.now()
			p.DisabledAt = &now
			changed = append(changed, "disabled")
		} else if !v && p.DisabledAt != nil {
			p.DisabledAt = nil
			changed = append(changed, "disabled")
		}
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	if len(changed) > 0 {
		p.UpdatedAt = s.now()
		err := s.store.InTx(r.Context(), func(tx *db.Store) error {
			if err := tx.UpdateProject(r.Context(), p); err != nil {
				return err
			}
			return s.audit(r, tx, "project.update", &p.ID, &p.ID, map[string]any{"fields": changed})
		})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectDTO(p))
}

func (s *Server) checkProfileExists(r *http.Request, id string) string {
	_, err := s.store.GetStorageProfile(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		return "storage profile not found"
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "lookup storage profile", "err", err)
		return "could not verify storage profile"
	}
	return ""
}

func validName(v string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "required"
	}
	if len([]rune(v)) > maxNameLen {
		return "", fmt.Sprintf("must be at most %d characters", maxNameLen)
	}
	return v, ""
}

// normalizeOrigins validates browser origins (scheme://host[:port]).
func normalizeOrigins(in []string) ([]string, string) {
	if len(in) > maxOrigins {
		return nil, fmt.Sprintf("at most %d origins", maxOrigins)
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, o := range in {
		o = strings.TrimSpace(o)
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Sprintf("invalid origin %q (expected scheme://host[:port])", o)
		}
		norm := strings.ToLower(u.Scheme + "://" + u.Host)
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out, ""
}

// ───────────── API keys ─────────────

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	keys, err := s.store.ListAPIKeys(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]apiKeyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyDTO(k))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[apiKeyDTO]{Items: out})
}

type createAPIKeyRequest struct {
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	Namespaces []string `json:"namespaces"`
	ExpiresAt  *string  `json:"expires_at"`
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	errs := map[string]string{}
	name, msg := validName(req.Name)
	if msg != "" {
		errs["name"] = msg
	}
	if len([]rune(name)) > maxAPIKeyName {
		errs["name"] = fmt.Sprintf("must be at most %d characters", maxAPIKeyName)
	}
	scopes := []string{}
	seen := map[string]bool{}
	if len(req.Scopes) == 0 {
		errs["scopes"] = "at least one scope is required"
	}
	for _, sc := range req.Scopes {
		if !auth.ValidScope(sc) {
			errs["scopes"] = fmt.Sprintf("unknown scope %q (allowed: %s)", sc, strings.Join(auth.AllScopes, ", "))
			break
		}
		if !seen[sc] {
			seen[sc] = true
			scopes = append(scopes, sc)
		}
	}
	var namespaces []string
	if len(req.Namespaces) > 100 {
		errs["namespaces"] = "at most 100 namespaces are allowed"
	} else if len(req.Namespaces) > 0 {
		seenNS := map[string]bool{}
		for _, raw := range req.Namespaces {
			ns := strings.TrimSpace(raw)
			if !pipeline.ValidNamespace(ns) {
				errs["namespaces"] = fmt.Sprintf("invalid namespace %q (1–128 characters of [A-Za-z0-9._:@-], starting with a letter or digit)", raw)
				break
			}
			if !seenNS[ns] {
				seenNS[ns] = true
				namespaces = append(namespaces, ns)
			}
		}
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			errs["expires_at"] = "must be an RFC 3339 timestamp"
		} else if !t.After(s.now()) {
			errs["expires_at"] = "must be in the future"
		} else {
			t = t.UTC()
			expiresAt = &t
		}
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}

	gen, err := auth.GenerateAPIKey(p.Environment, s.cfg.Pepper)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	k := &db.APIKey{ID: db.NewID(db.PrefixAPIKey), ProjectID: p.ID, Name: name, Prefix: gen.Prefix,
		SecretHash: gen.Hash, Scopes: scopes, Namespaces: namespaces, ExpiresAt: expiresAt, CreatedAt: s.now()}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.CreateAPIKey(r.Context(), k); err != nil {
			return err
		}
		details := map[string]any{"name": k.Name, "prefix": k.Prefix, "scopes": k.Scopes}
		if len(k.Namespaces) > 0 {
			details["namespaces"] = k.Namespaces
		}
		return s.audit(r, tx, "key.create", &p.ID, &k.ID, details)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"api_key": toAPIKeyDTO(k), "secret": gen.Secret})
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "key_id")
	k, err := s.store.GetAPIKey(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "api key")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if k.RevokedAt == nil {
		err = s.store.InTx(r.Context(), func(tx *db.Store) error {
			if err := tx.RevokeAPIKey(r.Context(), k.ID, s.now()); err != nil {
				return err
			}
			return s.audit(r, tx, "key.revoke", &k.ProjectID, &k.ID, map[string]any{"prefix": k.Prefix})
		})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ───────────── presets ─────────────

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	ps, err := s.store.ListPresets(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]presetDTO, 0, len(ps))
	for _, x := range ps {
		out = append(out, toPresetDTO(x))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[presetDTO]{Items: out})
}
