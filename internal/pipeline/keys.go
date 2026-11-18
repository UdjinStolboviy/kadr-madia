package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Storage key layout (docs/DATA_MODEL.md §Object keys):
//
//	{project}/{kind}/{yyyy}/{mm}/{blob}/{file}
//	{project}/{kind}/{yyyy}/{mm}/{blob}/hls/…
//	originals/{project}/{kind}/{yyyy}/{mm}/{blob}.{ext}
//
// The kind and month partitions make a bucket browsable and let lifecycle
// rules, cost reports and purges target a slice (e.g. old videos). The file
// name says what the object is (role, size, codec) so objects are
// distinguishable without the database. End-user file names are never part
// of a key: blobs are shared between assets (deduplication), names are
// personal data in public URLs, and a download gets its human name from
// Content-Disposition instead. Keys are stored per rendition, so blobs
// written with the earlier {project}/{blob}/{name}.{format} layout keep
// working unchanged.

// blobPrefix is the directory of a blob's renditions, with a trailing slash.
func blobPrefix(projectSlug string, b *db.Blob) string {
	t := b.CreatedAt.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return fmt.Sprintf("%s/%s/%04d/%02d/%s/", projectSlug, b.Kind, t.Year(), int(t.Month()), b.ID)
}

// originalKey is where a retained source file is stored (private bucket).
func originalKey(projectSlug string, b *db.Blob, ext string) string {
	return "originals/" + strings.TrimSuffix(blobPrefix(projectSlug, b), "/") + "." + ext
}

// renditionFile names one rendition object:
//
//	main-1080x720.avif    images, posters (poster-640x360.avif), thumbnails
//	main-720p-av1.mp4     video: height and codec
//	main.pdf              documents and anything without dimensions
func renditionFile(r producedFile) string {
	switch {
	case r.format == "mp4" && r.height > 0:
		return fmt.Sprintf("%s-%dp-%s.mp4", r.name, r.height, codecLabel(r.encoder))
	case r.width > 0 && r.height > 0:
		return fmt.Sprintf("%s-%dx%d.%s", r.name, r.width, r.height, r.format)
	default:
		return r.name + "." + r.format
	}
}

// codecLabel maps an encoder name to the codec it produces.
func codecLabel(encoder string) string {
	switch encoder {
	case "libsvtav1", "libaom-av1", "av1_vaapi":
		return "av1"
	default:
		return "h264"
	}
}
