package admin

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

const (
	previewMaxBytes     = 8 << 20
	previewDefaultWidth = 1024
	maxModerationLabels = 20
	maxModerationNote   = 1000
)

var labelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,39}$`)

type moderationDTO struct {
	AssetID   string   `json:"asset_id"`
	Status    string   `json:"status"` // unreviewed | approved | flagged | rejected
	Labels    []string `json:"labels"`
	Note      *string  `json:"note"`
	ActorType *string  `json:"actor_type"`
	ActorID   *string  `json:"actor_id"`
	UpdatedAt *string  `json:"updated_at"`
}

func toModerationDTO(assetID string, m *db.AssetModeration) moderationDTO {
	if m == nil {
		return moderationDTO{AssetID: assetID, Status: "unreviewed", Labels: []string{}}
	}
	return moderationDTO{AssetID: m.AssetID, Status: m.Status, Labels: m.Labels, Note: m.Note, ActorType: &m.ActorType,
		ActorID: m.ActorID, UpdatedAt: tsp(&m.UpdatedAt)}
}

type moderationItemDTO struct {
	AssetID    string        `json:"asset_id"`
	Kind       string        `json:"kind"`
	Namespace  string        `json:"namespace"`
	Visibility string        `json:"visibility"`
	CreatedAt  string        `json:"created_at"`
	Moderation moderationDTO `json:"moderation"`
}

// handleModerationQueue: GET /projects/{id}/moderation?status=unreviewed|approved|flagged|rejected|all&kind=&cursor=&limit=
func (s *Server) handleModerationQueue(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	st := q.Get("status")
	if !slices.Contains([]string{"", "unreviewed", "all", db.ModerationApproved, db.ModerationFlagged, db.ModerationRejected}, st) {
		validationFailed(w, map[string]string{"status": "must be unreviewed, approved, flagged, rejected or all"})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items, err := s.store.ListModerationQueue(r.Context(), db.ModerationFilter{ProjectID: p.ID, Status: st, Kind: q.Get("kind"),
		Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]moderationItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, moderationItemDTO{AssetID: it.AssetID, Kind: it.Kind, Namespace: it.Namespace, Visibility: it.Visibility,
			CreatedAt: ts(it.CreatedAt), Moderation: toModerationDTO(it.AssetID, it.Moderation)})
	}
	var next *string
	if len(items) == limit {
		next = &items[len(items)-1].AssetID
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

// loadProjectAsset returns the asset row when it belongs to the project.
func (s *Server) loadProjectAsset(w http.ResponseWriter, r *http.Request, projectID string) (*db.Asset, bool) {
	a, err := s.store.GetAsset(r.Context(), chi.URLParam(r, "asset_id"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && (a.ProjectID != projectID || a.Status == db.AssetDeleted)) {
		notFound(w, "asset")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return a, true
}

// handleGetModeration: GET /projects/{id}/assets/{asset_id}/moderation
func (s *Server) handleGetModeration(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	a, ok := s.loadProjectAsset(w, r, p.ID)
	if !ok {
		return
	}
	m, err := s.store.GetModeration(r.Context(), a.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toModerationDTO(a.ID, m))
}

// handleSetModeration: PUT /projects/{id}/assets/{asset_id}/moderation
// {status: approved|flagged|rejected, labels: [], note}. Emits asset.moderated.
func (s *Server) handleSetModeration(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	a, ok := s.loadProjectAsset(w, r, p.ID)
	if !ok {
		return
	}
	var in struct {
		Status string   `json:"status"`
		Labels []string `json:"labels"`
		Note   string   `json:"note"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	errs := map[string]string{}
	if !slices.Contains([]string{db.ModerationApproved, db.ModerationFlagged, db.ModerationRejected}, in.Status) {
		errs["status"] = "must be approved, flagged or rejected"
	}
	labels := []string{}
	for _, l := range in.Labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if !labelRe.MatchString(l) {
			errs["labels"] = "labels are lowercase words (a-z, 0-9, _ . : -), at most 40 characters"
			break
		}
		if !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	if len(labels) > maxModerationLabels {
		errs["labels"] = "at most 20 labels"
	}
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) > maxModerationNote {
		errs["note"] = "at most 1000 characters"
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	ctx := r.Context()
	m := &db.AssetModeration{AssetID: a.ID, ProjectID: p.ID, Status: in.Status, Labels: labels, ActorType: db.ActorAdmin, UpdatedAt: s.now()}
	if note != "" {
		m.Note = &note
	}
	if t := agentFrom(ctx); t != nil {
		m.ActorType = db.ActorAgent
	}
	id := actorID(ctx)
	m.ActorID = &id
	err := s.saveModeration(ctx, m, a.NS, func(tx *db.Store, prev string) error {
		return s.audit(r, tx, "asset.moderate", &p.ID, &a.ID, map[string]any{"status": m.Status, "previous_status": prev, "labels": m.Labels})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toModerationDTO(a.ID, m))
}

// saveModeration stores a decision, emits asset.moderated and lets audit
// record it, all in one transaction.
func (s *Server) saveModeration(ctx context.Context, m *db.AssetModeration, ns string, audit func(tx *db.Store, prev string) error) error {
	prev, err := s.store.GetModeration(ctx, m.AssetID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	prevStatus := "unreviewed"
	if prev != nil {
		prevStatus = prev.Status
	}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.UpsertModeration(ctx, m); err != nil {
			return err
		}
		if _, err := webhook.Emit(ctx, tx, m.ProjectID, webhook.EventAssetModerated, map[string]any{
			"asset_id": m.AssetID, "namespace": ns, "status": m.Status, "previous_status": prevStatus, "labels": m.Labels,
			"note": m.Note, "actor_type": m.ActorType,
		}, s.now()); err != nil {
			return err
		}
		return audit(tx, prevStatus)
	})
	if err == nil && s.cfg.Webhooks != nil {
		s.cfg.Webhooks.Notify()
	}
	return err
}

// handleAssetPreview: GET /projects/{id}/assets/{asset_id}/preview?width=1024 —
// an image of the asset (image rendition, video poster or document
// thumbnail). AVIF is converted to WebP when the image engine is available,
// so browsers and vision models can read it.
func (s *Server) handleAssetPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	data, format, err := s.assetPreview(r.Context(), p.ID, chi.URLParam(r, "asset_id"), atoiDefault(r.URL.Query().Get("width"), previewDefaultWidth))
	if err != nil {
		s.pipelineError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/"+format)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// assetPreview returns preview bytes and their format (webp, jpeg, png, gif
// or avif when no image engine is available).
func (s *Server) assetPreview(ctx context.Context, projectID, assetID string, width int) ([]byte, string, error) {
	if s.cfg.Pipeline == nil {
		return nil, "", &pipeline.Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Detail: "the media pipeline is not available"}
	}
	if width < 64 || width > 2048 {
		width = previewDefaultWidth
	}
	pv, err := s.cfg.Pipeline.ReadPreview(ctx, projectID, assetID, int64(width), previewMaxBytes)
	if err != nil {
		return nil, "", err
	}
	return pv.Data, pv.Format, nil
}
