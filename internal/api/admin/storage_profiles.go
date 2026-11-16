package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

var bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

var providers = map[string]bool{"r2": true, "s3": true, "b2": true, "hetzner": true, "garage": true}

// storageCredentials is the plaintext of storage_profiles.credentials_enc.
type storageCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// storageProfileInput is StorageProfileInput from docs/ADMIN_API.md.
type storageProfileInput struct {
	Name            string  `json:"name"`
	Provider        string  `json:"provider"`
	Endpoint        string  `json:"endpoint"`
	Region          string  `json:"region"`
	PublicBucket    string  `json:"public_bucket"`
	PrivateBucket   string  `json:"private_bucket"`
	PublicBaseURL   string  `json:"public_base_url"`
	PrivateBaseURL  *string `json:"private_base_url"`
	AccessKeyID     string  `json:"access_key_id"`
	SecretAccessKey string  `json:"secret_access_key"`
	IsPlatform      bool    `json:"is_platform"`
}

func credentialsAAD(profileID string) []byte { return []byte("storage_profile:" + profileID) }

func (s *Server) decryptCredentials(p *db.StorageProfile) (storageCredentials, error) {
	var c storageCredentials
	pt, err := s.cfg.Keyring.Decrypt(p.CredentialsEnc, credentialsAAD(p.ID))
	if err != nil {
		return c, fmt.Errorf("decrypt credentials of %s: %w", p.ID, err)
	}
	if err := json.Unmarshal(pt, &c); err != nil {
		return c, fmt.Errorf("decode credentials of %s: %w", p.ID, err)
	}
	return c, nil
}

func (s *Server) encryptCredentials(profileID string, c storageCredentials) ([]byte, error) {
	pt, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return s.cfg.Keyring.Encrypt(pt, credentialsAAD(profileID))
}

// maskKey renders an access key id as "AKIA…3F2Q".
func maskKey(k string) string {
	if len(k) <= 8 {
		return "…" + strings.Repeat("*", min(len(k), 4))
	}
	return k[:4] + "…" + k[len(k)-4:]
}

func (s *Server) toStorageProfileDTO(p *db.StorageProfile) storageProfileDTO {
	masked := ""
	if c, err := s.decryptCredentials(p); err == nil {
		masked = maskKey(c.AccessKeyID)
	} else {
		s.log.Error("storage profile credentials unreadable", "id", p.ID, "err", err)
		masked = "(unreadable)"
	}
	return storageProfileDTO{ID: p.ID, Name: p.Name, Provider: p.Provider, Endpoint: p.Endpoint, Region: p.Region,
		PublicBucket: p.PublicBucket, PrivateBucket: p.PrivateBucket, PublicBaseURL: p.PublicBaseURL,
		PrivateBaseURL: p.PrivateBaseURL, AccessKeyID: masked, IsPlatform: p.IsPlatform,
		LastTestAt: tsp(p.LastTestAt), LastTestOK: p.LastTestOK, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt)}
}

// validate normalises in and returns field errors. requireCreds is true on create.
func (in *storageProfileInput) validate(requireCreds bool) map[string]string {
	errs := map[string]string{}
	var msg string
	if in.Name, msg = validName(in.Name); msg != "" {
		errs["name"] = msg
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if !providers[in.Provider] {
		errs["provider"] = "must be one of r2, s3, b2, hetzner, garage"
	}
	if u, m := validHTTPURL(in.Endpoint); m != "" {
		errs["endpoint"] = m
	} else {
		in.Endpoint = u
	}
	in.Region = strings.TrimSpace(in.Region)
	if in.Region == "" {
		in.Region = "auto"
	}
	if len(in.Region) > 64 {
		errs["region"] = "too long"
	}
	in.PublicBucket = strings.TrimSpace(in.PublicBucket)
	in.PrivateBucket = strings.TrimSpace(in.PrivateBucket)
	if !bucketRe.MatchString(in.PublicBucket) {
		errs["public_bucket"] = "invalid bucket name"
	}
	if !bucketRe.MatchString(in.PrivateBucket) {
		errs["private_bucket"] = "invalid bucket name"
	} else if in.PrivateBucket == in.PublicBucket {
		errs["private_bucket"] = "must differ from public_bucket (public and private data are physically separated)"
	}
	if u, m := validHTTPURL(in.PublicBaseURL); m != "" {
		errs["public_base_url"] = m
	} else {
		in.PublicBaseURL = u
	}
	if in.PrivateBaseURL != nil {
		if strings.TrimSpace(*in.PrivateBaseURL) == "" {
			in.PrivateBaseURL = nil
		} else if u, m := validHTTPURL(*in.PrivateBaseURL); m != "" {
			errs["private_base_url"] = m
		} else {
			in.PrivateBaseURL = &u
		}
	}
	in.AccessKeyID = strings.TrimSpace(in.AccessKeyID)
	in.SecretAccessKey = strings.TrimSpace(in.SecretAccessKey)
	if requireCreds {
		if in.AccessKeyID == "" {
			errs["access_key_id"] = "required"
		}
		if in.SecretAccessKey == "" {
			errs["secret_access_key"] = "required"
		}
	}
	if len(in.AccessKeyID) > 256 {
		errs["access_key_id"] = "too long"
	}
	if len(in.SecretAccessKey) > 256 {
		errs["secret_access_key"] = "too long"
	}
	return errs
}

func validHTTPURL(v string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "required"
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return "", "must be an http(s) URL"
	}
	return strings.TrimRight(v, "/"), ""
}

func (s *Server) handleListStorageProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListStorageProfiles(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]storageProfileDTO, 0, len(ps))
	for _, p := range ps {
		out = append(out, s.toStorageProfileDTO(p))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[storageProfileDTO]{Items: out})
}

func (s *Server) handleCreateStorageProfile(w http.ResponseWriter, r *http.Request) {
	var in storageProfileInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	if errs := in.validate(true); len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	now := s.now()
	p := &db.StorageProfile{ID: db.NewID(db.PrefixStorageProfile), Name: in.Name, Provider: in.Provider,
		Endpoint: in.Endpoint, Region: in.Region, PublicBucket: in.PublicBucket, PrivateBucket: in.PrivateBucket,
		PublicBaseURL: in.PublicBaseURL, PrivateBaseURL: in.PrivateBaseURL, IsPlatform: in.IsPlatform,
		CreatedAt: now, UpdatedAt: now}
	enc, err := s.encryptCredentials(p.ID, storageCredentials{AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p.CredentialsEnc = enc
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.CreateStorageProfile(r.Context(), p); err != nil {
			return err
		}
		return s.audit(r, tx, "storage_profile.create", nil, &p.ID, map[string]any{"name": p.Name, "provider": p.Provider})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, s.toStorageProfileDTO(p))
}

func (s *Server) loadStorageProfile(w http.ResponseWriter, r *http.Request) (*db.StorageProfile, bool) {
	p, err := s.store.GetStorageProfile(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "storage profile")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return p, true
}

func (s *Server) handleGetStorageProfile(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.loadStorageProfile(w, r); ok {
		httpx.WriteJSON(w, http.StatusOK, s.toStorageProfileDTO(p))
	}
}

// handlePatchStorageProfile merges a partial input onto the stored profile.
// Credentials change only when access_key_id and/or secret_access_key are
// present and non-empty.
func (s *Server) handlePatchStorageProfile(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		badJSON(w, err)
		return
	}
	p, ok := s.loadStorageProfile(w, r)
	if !ok {
		return
	}
	creds, err := s.decryptCredentials(p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	in := storageProfileInput{Name: p.Name, Provider: p.Provider, Endpoint: p.Endpoint, Region: p.Region,
		PublicBucket: p.PublicBucket, PrivateBucket: p.PrivateBucket, PublicBaseURL: p.PublicBaseURL,
		PrivateBaseURL: p.PrivateBaseURL, IsPlatform: p.IsPlatform}

	errs := map[string]string{}
	str := func(field string, dst *string) {
		if raw, ok := body[field]; ok {
			if err := json.Unmarshal(raw, dst); err != nil {
				errs[field] = "must be a string"
			}
		}
	}
	str("name", &in.Name)
	str("provider", &in.Provider)
	str("endpoint", &in.Endpoint)
	str("region", &in.Region)
	str("public_bucket", &in.PublicBucket)
	str("private_bucket", &in.PrivateBucket)
	str("public_base_url", &in.PublicBaseURL)
	str("access_key_id", &in.AccessKeyID)
	str("secret_access_key", &in.SecretAccessKey)
	if raw, ok := body["private_base_url"]; ok {
		if string(raw) == "null" {
			in.PrivateBaseURL = nil
		} else {
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				errs["private_base_url"] = "must be a string or null"
			} else {
				in.PrivateBaseURL = &v
			}
		}
	}
	if raw, ok := body["is_platform"]; ok {
		if err := json.Unmarshal(raw, &in.IsPlatform); err != nil {
			errs["is_platform"] = "must be a boolean"
		}
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	if verrs := in.validate(false); len(verrs) > 0 {
		validationFailed(w, verrs)
		return
	}

	credsChanged := false
	// A UI that round-trips the form may send back the masked id ("AKIA…3F2Q").
	if strings.Contains(in.AccessKeyID, "…") {
		in.AccessKeyID = ""
	}
	if in.AccessKeyID != "" && in.AccessKeyID != creds.AccessKeyID {
		creds.AccessKeyID, credsChanged = in.AccessKeyID, true
	}
	if in.SecretAccessKey != "" && in.SecretAccessKey != creds.SecretAccessKey {
		creds.SecretAccessKey, credsChanged = in.SecretAccessKey, true
	}
	if credsChanged {
		enc, err := s.encryptCredentials(p.ID, creds)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		p.CredentialsEnc = enc
	}
	p.Name, p.Provider, p.Endpoint, p.Region = in.Name, in.Provider, in.Endpoint, in.Region
	p.PublicBucket, p.PrivateBucket, p.PublicBaseURL, p.PrivateBaseURL = in.PublicBucket, in.PrivateBucket, in.PublicBaseURL, in.PrivateBaseURL
	p.IsPlatform = in.IsPlatform
	p.UpdatedAt = s.now()

	fields := make([]string, 0, len(body))
	for k := range body {
		fields = append(fields, k) // names only, never values
	}
	sort.Strings(fields)
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.UpdateStorageProfile(r.Context(), p); err != nil {
			return err
		}
		return s.audit(r, tx, "storage_profile.update", nil, &p.ID, map[string]any{"fields": fields, "credentials_changed": credsChanged})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.toStorageProfileDTO(p))
}

func (s *Server) handleDeleteStorageProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadStorageProfile(w, r)
	if !ok {
		return
	}
	n, err := s.store.CountStorageProfileUsage(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if n > 0 {
		httpx.WriteProblem(w, http.StatusConflict, "in_use", "storage profile is referenced by projects or stored blobs")
		return
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.DeleteStorageProfile(r.Context(), p.ID); err != nil {
			return err
		}
		return s.audit(r, tx, "storage_profile.delete", nil, &p.ID, map[string]any{"name": p.Name})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestStorageProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadStorageProfile(w, r)
	if !ok {
		return
	}
	creds, err := s.decryptCredentials(p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var result storage.TestResult
	drv, err := s.cfg.NewDriver(storage.Config{Provider: p.Provider, Endpoint: p.Endpoint, Region: p.Region,
		AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey})
	if err != nil {
		result = storage.TestResult{Checks: []storage.CheckResult{{Name: "configure", Detail: err.Error()}}}
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		result = storage.RunChecks(ctx, storage.CheckTarget{Driver: drv, PublicBucket: p.PublicBucket,
			PrivateBucket: p.PrivateBucket, PublicBaseURL: p.PublicBaseURL, HTTPClient: s.cfg.HTTPClient})
	}
	now := s.now()
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SetStorageProfileTestResult(r.Context(), p.ID, now, result.OK); err != nil {
			return err
		}
		return s.audit(r, tx, "storage_profile.test", nil, &p.ID, map[string]any{"ok": result.OK})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}
