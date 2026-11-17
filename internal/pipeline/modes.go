package pipeline

import (
	"context"
	"errors"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// EnsureModePresets adds the compression-mode presets (image-compact …
// video-quality) to projects created before modes existed. Existing presets,
// edited or not, are never touched. It returns the number of presets created.
func (s *Service) EnsureModePresets(ctx context.Context) (int, error) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range projects {
		for _, dp := range preset.ModePresets() {
			if _, err := s.store.GetPresetBySlug(ctx, p.ID, dp.Slug); err == nil {
				continue
			} else if !errors.Is(err, db.ErrNotFound) {
				return n, err
			}
			err := s.store.CreatePreset(ctx, &db.Preset{ID: db.NewID(db.PrefixPreset), ProjectID: p.ID, Slug: dp.Slug, Kind: dp.Kind,
				Description: dp.Description, CurrentVersion: 1, Params: dp.Params, CreatedAt: s.now()}, "system")
			if err != nil && !errors.Is(err, db.ErrConflict) {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
