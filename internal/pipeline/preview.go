package pipeline

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// previewFormats are rendition formats that can be shown as an image, best
// first for consumers (browsers and vision models read webp/jpeg/png/gif;
// AVIF needs a conversion by the caller).
var previewFormats = []string{"webp", "jpeg", "jpg", "png", "gif", "avif"}

// Preview is one image rendition of an asset, read from storage.
type Preview struct {
	Name   string
	Format string
	Width  *int64
	Height *int64
	Data   []byte
}

// ReadPreview returns the image rendition of an asset best suited for a
// preview of about targetWidth pixels (image renditions, video posters and
// document thumbnails). It reads at most maxBytes.
func (s *Service) ReadPreview(ctx context.Context, projectID, assetID string, targetWidth int64, maxBytes int64) (*Preview, error) {
	a, err := s.loadAsset(ctx, projectID, assetID)
	if err != nil {
		return nil, err
	}
	if a.BlobID == nil {
		return nil, apiErr(http.StatusConflict, "not_ready", "asset %s has no renditions yet", assetID)
	}
	b, err := s.store.GetBlob(ctx, *a.BlobID)
	if err != nil {
		return nil, err
	}
	rs, err := s.store.ListRenditions(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	best := -1
	score := func(i int) int64 {
		r := rs[i]
		var w int64
		if r.Width != nil {
			w = *r.Width
		}
		// Prefer a width at or below the target, the closest one; larger ones rank after.
		d := targetWidth - w
		if d < 0 {
			d = 1<<40 - d
		}
		if r.Format == "avif" { // needs a conversion: only when nothing else exists
			d += 1 << 50
		}
		return d
	}
	for i, r := range rs {
		if !slices.Contains(previewFormats, r.Format) || r.Bytes > maxBytes {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		if score(i) < score(best) {
			best = i
		}
	}
	if best < 0 {
		return nil, apiErr(http.StatusUnprocessableEntity, "no_preview", "asset %s has no image rendition small enough to preview", assetID)
	}
	r := rs[best]
	tgt, err := s.storageByProfile(ctx, b.StorageProfileID)
	if err != nil {
		return nil, err
	}
	body, _, err := tgt.driver.Get(ctx, tgt.bucket(b.Visibility), r.StorageKey)
	if err != nil {
		return nil, fmt.Errorf("preview %s: get %s: %w", assetID, r.StorageKey, err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("preview %s: read: %w", assetID, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, apiErr(http.StatusUnprocessableEntity, "no_preview", "the preview rendition of %s is too large", assetID)
	}
	return &Preview{Name: r.Name, Format: r.Format, Width: r.Width, Height: r.Height, Data: data}, nil
}
