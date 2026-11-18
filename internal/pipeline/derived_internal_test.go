package pipeline

import (
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func TestDerivedImageFormats(t *testing.T) {
	formats := func(o []preset.ImageOutput) (out []string) {
		for _, x := range o {
			out = append(out, x.Name+"."+x.Format)
		}
		return out
	}
	if got := formats(posterParams(preset.VideoParams{}).Outputs); len(got) != 2 || got[0] != "poster.avif" || got[1] != "poster.webp" {
		t.Fatalf("default poster: %v", got)
	}
	v := preset.VideoParams{Poster: preset.Poster{Formats: []string{"avif"}}}
	if got := formats(posterParams(v).Outputs); len(got) != 1 || got[0] != "poster.avif" {
		t.Fatalf("avif-only poster: %v", got)
	}
	f := preset.FileParams{Thumbnail: preset.FileThumb{Enabled: true, Formats: []string{"avif"}}}
	if got := formats(thumbParams(f).Outputs); len(got) != 1 || got[0] != "thumb.avif" {
		t.Fatalf("avif-only thumb: %v", got)
	}
}
