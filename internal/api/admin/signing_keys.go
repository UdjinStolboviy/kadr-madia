package admin

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

type signingKeyDTO struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Status      string  `json:"status"`
	PublishedAt *string `json:"published_at"`
	CreatedAt   string  `json:"created_at"`
}

func toSigningKeyDTO(k *db.SigningKey) signingKeyDTO {
	return signingKeyDTO{ID: k.ID, ProjectID: k.ProjectID, Status: k.Status, PublishedAt: tsp(k.PublishedAt), CreatedAt: ts(k.CreatedAt)}
}

// handleListSigningKeys: GET /admin/api/projects/{id}/signing-keys.
func (s *Server) handleListSigningKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	ks, err := s.store.ListSigningKeys(r.Context(), p.ID, true)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]signingKeyDTO, 0, len(ks))
	for _, k := range ks {
		out = append(out, toSigningKeyDTO(k))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleCreateSigningKey: POST /admin/api/projects/{id}/signing-keys creates a
// new active key (rotation: the previous active key becomes verify_only). The
// secret is returned once and also available to the edge via edge-config.
func (s *Server) handleCreateSigningKey(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.internalError(w, r, err)
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	n, _ := s.store.ListSigningKeys(r.Context(), p.ID, true)
	kid := p.Slug + "-k" + strconv.Itoa(len(n)+1)
	enc, err := s.cfg.Keyring.Encrypt([]byte(secret), pipeline.SigningKeySecretAAD(kid))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	k := &db.SigningKey{ID: kid, ProjectID: p.ID, SecretEnc: enc, Status: db.SigningActive, CreatedAt: s.now()}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.RotateSigningKey(r.Context(), k); err != nil {
			return err
		}
		return s.audit(r, tx, "signing_key.create", &p.ID, &kid, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(k), "secret": secret})
}

// handleRetireSigningKey: DELETE /admin/api/signing-keys/{kid}.
func (s *Server) handleRetireSigningKey(w http.ResponseWriter, r *http.Request) {
	kid := chi.URLParam(r, "kid")
	k, err := s.store.GetSigningKey(r.Context(), kid)
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "signing key")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if _, err := tx.RetireSigningKey(r.Context(), kid); err != nil {
			return err
		}
		return s.audit(r, tx, "signing_key.retire", &k.ProjectID, &kid, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePublishSigningKeys: POST /admin/api/signing-keys/publish pushes the
// active and verify_only keys of all projects to kadr-edge (SIGNING_KEYS).
func (s *Server) handlePublishSigningKeys(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Edge.Configured() {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "edge_not_configured", "set KADR_CF_ACCOUNT_ID and KADR_CF_API_TOKEN to publish keys to kadr-edge")
		return
	}
	ks, err := s.store.ListSigningKeys(r.Context(), "", false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	m := make(map[string]string, len(ks))
	for _, k := range ks {
		sec, err := s.cfg.Keyring.Decrypt(k.SecretEnc, pipeline.SigningKeySecretAAD(k.ID))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		m[k.ID] = string(sec)
	}
	if err := s.cfg.Edge.Publish(r.Context(), m); err != nil {
		s.log.WarnContext(r.Context(), "publish signing keys", "err", err)
		httpx.WriteProblem(w, http.StatusBadGateway, "edge_publish_failed", err.Error())
		return
	}
	_ = s.store.MarkSigningKeysPublished(r.Context(), s.now())
	_ = s.audit(r, s.store, "signing_key.publish", nil, nil, map[string]any{"count": len(m)})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"published": len(m)})
}

// handleEdgeConfig: GET /admin/api/signing-keys/edge-config returns the
// kid → secret map for the kadr-edge SIGNING_KEYS secret (active and
// verify_only keys of every project). Audited; the response is never cached.
func (s *Server) handleEdgeConfig(w http.ResponseWriter, r *http.Request) {
	ks, err := s.store.ListSigningKeys(r.Context(), "", false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	m := make(map[string]string, len(ks))
	for _, k := range ks {
		sec, err := s.cfg.Keyring.Decrypt(k.SecretEnc, pipeline.SigningKeySecretAAD(k.ID))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		m[k.ID] = string(sec)
	}
	_ = s.audit(r, s.store, "signing_key.export", nil, nil, map[string]any{"count": len(m)})
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, m)
}
