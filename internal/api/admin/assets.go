package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

// handleListAssets: GET /admin/api/projects/{id}/assets (asset explorer).
func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if s.cfg.Pipeline == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "the media pipeline is not available")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, err := s.cfg.Pipeline.ListAssets(r.Context(), p.ID, pipeline.ListAssetsInput{
		Namespace: q.Get("namespace"), Kind: q.Get("kind"), Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: limit,
	})
	if err != nil {
		s.pipelineError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

// handleGetAsset: GET /admin/api/projects/{id}/assets/{asset_id}.
func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok || s.cfg.Pipeline == nil {
		return
	}
	a, err := s.cfg.Pipeline.GetAsset(r.Context(), p.ID, chi.URLParam(r, "asset_id"))
	if err != nil {
		s.pipelineError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

// handleDeleteAsset: DELETE /admin/api/projects/{id}/assets/{asset_id}.
func (s *Server) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok || s.cfg.Pipeline == nil {
		return
	}
	id := chi.URLParam(r, "asset_id")
	if err := s.cfg.Pipeline.DeleteAsset(r.Context(), p.ID, id); err != nil {
		s.pipelineError(w, r, err)
		return
	}
	_ = s.audit(r, s.store, "asset.delete", &p.ID, &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pipelineError(w http.ResponseWriter, r *http.Request, err error) {
	var pe *pipeline.Error
	if errors.As(err, &pe) {
		httpx.WriteProblem(w, pe.Status, pe.Code, pe.Detail)
		return
	}
	s.internalError(w, r, err)
}

type usageDayDTO struct {
	Day          string `json:"day"`
	Uploads      int64  `json:"uploads"`
	AssetsReady  int64  `json:"assets_ready"`
	AssetsFailed int64  `json:"assets_failed"`
	BytesIn      int64  `json:"bytes_in"`
	BytesStored  int64  `json:"bytes_stored"`
	VideoSec     int64  `json:"video_sec"`
	CPUMillis    int64  `json:"cpu_ms"`
	DedupHits    int64  `json:"dedup_hits"`
}

// handleAnalytics: GET /admin/api/projects/{id}/analytics?days=30 —
// per-day usage plus totals, savings and failure rate.
func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 30
	}
	to := s.now().UTC()
	from := to.AddDate(0, 0, -(days - 1))
	rows, err := s.store.ListUsageByDay(r.Context(), p.ID, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var tot usageDayDTO
	items := make([]usageDayDTO, 0, len(rows))
	for _, x := range rows {
		d := usageDayDTO{Day: x.Day, Uploads: x.Uploads, AssetsReady: x.AssetsReady, AssetsFailed: x.AssetsFailed, BytesIn: x.BytesIn,
			BytesStored: x.BytesStored, VideoSec: x.VideoSec, CPUMillis: x.CPUMillis, DedupHits: x.DedupHits}
		items = append(items, d)
		tot.Uploads += d.Uploads
		tot.AssetsReady += d.AssetsReady
		tot.AssetsFailed += d.AssetsFailed
		tot.BytesIn += d.BytesIn
		tot.BytesStored += d.BytesStored
		tot.VideoSec += d.VideoSec
		tot.CPUMillis += d.CPUMillis
		tot.DedupHits += d.DedupHits
	}
	var savings, failRate float64
	if tot.BytesIn > 0 {
		savings = 1 - float64(tot.BytesStored)/float64(tot.BytesIn)
	}
	if n := tot.AssetsReady + tot.AssetsFailed; n > 0 {
		failRate = float64(tot.AssetsFailed) / float64(n)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "days": items,
		"totals": tot, "savings_ratio": savings, "failed_rate": failRate,
	})
}
