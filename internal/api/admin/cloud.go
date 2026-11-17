package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

type cloudConnectionDTO struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	Namespace    *string `json:"namespace"`
	Provider     string  `json:"provider"`
	AccountLabel *string `json:"account_label"`
	CreatedAt    string  `json:"created_at"`
}

func toCloudDTO(c *db.CloudConnection) cloudConnectionDTO {
	return cloudConnectionDTO{ID: c.ID, ProjectID: c.ProjectID, Namespace: c.NS, Provider: c.Provider, AccountLabel: c.AccountLabel, CreatedAt: ts(c.CreatedAt)}
}

func (s *Server) handleListCloudConnections(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	cs, err := s.store.ListCloudConnections(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]cloudConnectionDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, toCloudDTO(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleCreateCloudConnection: POST /admin/api/projects/{id}/cloud-connections
// { provider, account_label?, namespace?, token } where token is the rclone
// OAuth token JSON (`rclone authorize "drive"`). It is stored encrypted.
func (s *Server) handleCreateCloudConnection(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var in struct {
		Provider     string  `json:"provider"`
		AccountLabel *string `json:"account_label"`
		Namespace    *string `json:"namespace"`
		Token        string  `json:"token"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	errs := map[string]string{}
	switch in.Provider {
	case "gdrive", "onedrive", "dropbox":
	default:
		errs["provider"] = "must be gdrive, onedrive or dropbox"
	}
	var probe map[string]any
	if json.Unmarshal([]byte(in.Token), &probe) != nil || probe["access_token"] == nil {
		errs["token"] = "must be the rclone token JSON containing access_token"
	}
	if in.Namespace != nil && !pipeline.ValidNamespace(*in.Namespace) {
		errs["namespace"] = "invalid namespace"
	}
	if len(errs) > 0 {
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"type": "about:blank", "title": "Unprocessable Entity",
			"status": 422, "code": "validation_failed", "detail": "request validation failed", "errors": errs})
		return
	}
	id := db.NewID(db.PrefixCloudConnection)
	enc, err := s.cfg.Keyring.Encrypt([]byte(in.Token), pipeline.CloudTokenAAD(id))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	c := &db.CloudConnection{ID: id, ProjectID: p.ID, NS: in.Namespace, Provider: in.Provider, AccountLabel: in.AccountLabel, TokenEnc: enc, CreatedAt: s.now()}
	if c.AccountLabel != nil {
		l := strings.TrimSpace(*c.AccountLabel)
		c.AccountLabel = &l
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.CreateCloudConnection(r.Context(), c); err != nil {
			return err
		}
		return s.audit(r, tx, "cloud_connection.create", &p.ID, &id, map[string]any{"provider": in.Provider})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toCloudDTO(c))
}

func (s *Server) handleRevokeCloudConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := s.store.GetCloudConnection(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "connection")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if _, err := tx.RevokeCloudConnection(r.Context(), id, s.now()); err != nil {
			return err
		}
		return s.audit(r, tx, "cloud_connection.revoke", &c.ProjectID, &id, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
