package pipeline

import (
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

func TestStorageKeys(t *testing.T) {
	b := &db.Blob{ID: "blb_1", Kind: "video", CreatedAt: time.Date(2026, 3, 9, 23, 0, 0, 0, time.FixedZone("x", 3*3600))}
	if got := blobPrefix("omi-prod", b); got != "omi-prod/video/2026/03/blb_1/" {
		t.Fatalf("prefix %q", got)
	}
	if got := originalKey("omi-prod", b, "mov"); got != "originals/omi-prod/video/2026/03/blb_1.mov" {
		t.Fatalf("original %q", got)
	}
	cases := map[string]producedFile{
		"main-720p-av1.mp4":   {name: "main", format: "mp4", width: 1280, height: 720, encoder: "libsvtav1"},
		"main-480p-h264.mp4":  {name: "main", format: "mp4", width: 854, height: 480, encoder: "h264_vaapi"},
		"poster-640x360.avif": {name: "poster", format: "avif", width: 640, height: 360},
		"main-1080x1350.avif": {name: "main", format: "avif", width: 1080, height: 1350},
		"thumb-480x679.webp":  {name: "thumb", format: "webp", width: 480, height: 679},
		"main.pdf":            {name: "main", format: "pdf"},
	}
	for want, f := range cases {
		if got := renditionFile(f); got != want {
			t.Errorf("renditionFile(%+v) = %q, want %q", f, got, want)
		}
	}
}
