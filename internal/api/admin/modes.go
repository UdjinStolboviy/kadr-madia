package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

type modeDTO struct {
	Mode        string     `json:"mode"`
	Description string     `json:"description"`
	Image       presetDTO  `json:"image"`
	Video       presetDTO  `json:"video"`
	File        presetDTO  `json:"file"`
	Customized  customized `json:"customized"`
}

type customized struct {
	Image bool `json:"image"`
	Video bool `json:"video"`
	File  bool `json:"file"`
}

// handleListModes: GET /admin/api/projects/{id}/modes — the three compression
// modes of the project with their image and video presets and whether the
// project edited them (current_version > 1, or params differing from defaults).
func (s *Server) handleListModes(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	defaults := map[string]preset.DefaultPreset{}
	for _, d := range preset.ModePresets() {
		defaults[d.Slug] = d
	}
	descr := map[string]string{
		preset.ModeCompact:  "Good quality at a very small size",
		preset.ModeBalanced: "The golden middle",
		preset.ModeQuality:  "Ideal quality at the smallest size that achieves it",
	}
	out := make([]modeDTO, 0, 3)
	for _, m := range preset.Modes {
		d := modeDTO{Mode: m, Description: descr[m]}
		for _, kind := range []string{"image", "video", "file"} {
			pr, err := s.store.GetPresetBySlug(r.Context(), p.ID, preset.ModeSlug(kind, m))
			if errors.Is(err, db.ErrNotFound) {
				continue // created on the next start (EnsureModePresets)
			}
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			dto := toPresetDTO(pr)
			edited := pr.CurrentVersion > 1 && !sameJSON(pr.Params, defaults[pr.Slug].Params)
			switch kind {
			case "image":
				d.Image, d.Customized.Image = dto, edited
			case "video":
				d.Video, d.Customized.Video = dto, edited
			default:
				d.File, d.Customized.File = dto, edited
			}
		}
		out = append(out, d)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"default_mode": p.DefaultMode, "modes": out})
}

// handleResetPreset: POST /admin/api/projects/{id}/presets/{slug}/reset —
// restores a built-in preset to its defaults as a new version.
func (s *Server) handleResetPreset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")
	var def *preset.DefaultPreset
	for _, d := range preset.DefaultPresets() {
		if d.Slug == slug {
			d := d
			def = &d
		}
	}
	if def == nil {
		validationFailed(w, map[string]string{"slug": "only built-in presets can be reset"})
		return
	}
	existing, err := s.store.GetPresetBySlug(r.Context(), p.ID, slug)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	s.savePresetVersion(w, r, p, existing, slug, def.Kind, def.Description, def.Params)
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xa) == string(yb)
}
