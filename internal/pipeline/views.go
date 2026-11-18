package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// JSON shapes of docs/API.md and api/openapi.yaml. Times are RFC 3339 (UTC).

// AssetView is the public "asset" object.
type AssetView struct {
	ID            string           `json:"id"`
	Status        string           `json:"status"`
	Kind          string           `json:"kind"`
	Namespace     string           `json:"namespace"`
	Visibility    string           `json:"visibility"`
	Preset        string           `json:"preset"`
	PresetVersion int              `json:"preset_version"`
	Original      OriginalView     `json:"original"`
	Renditions    []RenditionView  `json:"renditions"`
	Placeholder   *PlaceholderView `json:"placeholder"`
	DurationSec   *float64         `json:"duration_sec"`
	Poster        *RenditionView   `json:"poster"`
	Metadata      json.RawMessage  `json:"metadata"`
	SavingsRatio  *float64         `json:"savings_ratio"`
	Error         *AssetError      `json:"error,omitempty"`
	CreatedAt     string           `json:"created_at"`
	ReadyAt       *string          `json:"ready_at"`
}

// OriginalView describes the source file.
type OriginalView struct {
	Filename    *string `json:"filename"`
	ContentType *string `json:"content_type"`
	Bytes       int64   `json:"bytes"`
	Width       *int64  `json:"width"`
	Height      *int64  `json:"height"`
}

// RenditionView is one stored file. Public renditions carry url; private
// ones carry key (to be signed by the SDK or POST /v1/signed-urls).
type RenditionView struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Bytes  int64  `json:"bytes"`
	Width  *int64 `json:"width"`
	Height *int64 `json:"height"`
	URL    string `json:"url,omitempty"`
	Key    string `json:"key,omitempty"`
	// Encoder is the video encoder that produced the rendition (h264_vaapi | libx264 | libsvtav1).
	Encoder string `json:"encoder,omitempty"`
}

// PlaceholderView carries the base64 ThumbHash and the dominant color.
type PlaceholderView struct {
	ThumbHash     *string `json:"thumbhash"`
	DominantColor *string `json:"dominant_color"`
}

// AssetError explains a failed asset.
type AssetError struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339p(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

// assetView renders an asset using store (which may be a transaction).
func (s *Service) assetView(ctx context.Context, store *db.Store, a *db.Asset) (*AssetView, error) {
	v := &AssetView{
		ID: a.ID, Status: a.Status, Kind: a.Kind, Namespace: a.NS, Visibility: a.Visibility,
		Renditions: []RenditionView{}, Metadata: a.Metadata, CreatedAt: rfc3339(a.CreatedAt), ReadyAt: rfc3339p(a.ReadyAt),
	}
	if len(v.Metadata) == 0 {
		v.Metadata = json.RawMessage("{}")
	}
	v.Original.Filename = a.Filename
	if a.PresetID != nil && a.PresetVersion != nil {
		v.PresetVersion = int(*a.PresetVersion)
		if pv, err := store.GetPresetVersion(ctx, *a.PresetID, int(*a.PresetVersion)); err == nil {
			v.Preset = pv.Slug
		} else if !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
	}
	if a.Status == db.AssetFailed && a.ErrorCode != nil {
		v.Error = &AssetError{Code: *a.ErrorCode}
		if a.ErrorDetail != nil {
			v.Error.Detail = *a.ErrorDetail
		}
	}
	if a.UploadID != nil {
		if u, err := store.GetUpload(ctx, *a.UploadID); err == nil {
			v.Original.ContentType = u.ContentType
			v.Original.Bytes = u.DeclaredBytes
		} else if !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
	}
	if a.BlobID == nil {
		return v, nil
	}
	b, err := store.GetBlob(ctx, *a.BlobID)
	if err != nil {
		return nil, err
	}
	v.Original.Bytes = b.OriginalBytes
	v.Original.Width, v.Original.Height = b.OriginalWidth, b.OriginalHeight
	if b.OriginalContentType != nil {
		v.Original.ContentType = b.OriginalContentType
	}
	if b.DurationMs != nil {
		v.DurationSec = ptr(float64(*b.DurationMs) / 1000)
	}
	v.Placeholder = &PlaceholderView{DominantColor: b.DominantColor}
	if len(b.ThumbHash) > 0 {
		v.Placeholder.ThumbHash = ptr(base64.StdEncoding.EncodeToString(b.ThumbHash))
	}
	if b.OriginalBytes > 0 {
		r := 1 - float64(b.TotalBytes)/float64(b.OriginalBytes)
		r = math.Round(r*1000) / 1000
		v.SavingsRatio = &r
	}
	rs, err := store.ListRenditions(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	var tgt *target
	if b.Visibility == "public" && a.Status != db.AssetDeleted {
		if tgt, err = s.storageByProfile(ctx, b.StorageProfileID); err != nil {
			return nil, err
		}
	}
	for _, r := range rs {
		rv := RenditionView{Name: r.Name, Format: r.Format, Bytes: r.Bytes, Width: r.Width, Height: r.Height}
		if r.Encoder != nil {
			rv.Encoder = *r.Encoder
		}
		if tgt != nil {
			rv.URL = tgt.publicURL(r.StorageKey)
		} else {
			rv.Key = r.StorageKey
		}
		v.Renditions = append(v.Renditions, rv)
	}
	v.Poster = pickPoster(v.Renditions)
	return v, nil
}

// pickPoster returns the video poster rendition (AVIF preferred, the WebP
// fallback otherwise), or nil. It is also listed in renditions.
func pickPoster(rs []RenditionView) *RenditionView {
	var found *RenditionView
	for i := range rs {
		if rs[i].Name != "poster" {
			continue
		}
		if rs[i].Format == "avif" {
			p := rs[i]
			return &p
		}
		if found == nil {
			p := rs[i]
			found = &p
		}
	}
	return found
}

// PresetView is a preset as returned by GET /v1/presets.
type PresetView struct {
	Slug        string          `json:"slug"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	Version     int             `json:"version"`
	Params      json.RawMessage `json:"params"`
	CreatedAt   string          `json:"created_at"`
}

func presetView(p *db.Preset) PresetView {
	params := p.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return PresetView{Slug: p.Slug, Kind: p.Kind, Description: p.Description, Version: p.CurrentVersion, Params: params,
		CreatedAt: rfc3339(p.CreatedAt)}
}

// UsageView is the body of GET /v1/namespaces/{ns}/usage.
type UsageView struct {
	Namespace          string `json:"namespace"`
	UsedBytes          int64  `json:"used_bytes"`
	ReservedBytes      int64  `json:"reserved_bytes"`
	AssetCount         int64  `json:"asset_count"`
	VideoSecMonth      int64  `json:"video_sec_month"`
	QuotaBytes         *int64 `json:"quota_bytes"`
	QuotaAssets        *int64 `json:"quota_assets"`
	QuotaVideoSecMonth *int64 `json:"quota_video_sec_month"`
}

// UploadTicket is the 201 body of POST /v1/uploads.
type UploadTicket struct {
	UploadID  string            `json:"upload_id"`
	Method    string            `json:"method"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt string            `json:"expires_at"`
	// Multipart tickets: upload each part with PUT, then complete with the ETags.
	PartSize int64        `json:"part_size,omitempty"`
	Parts    []PartTicket `json:"parts,omitempty"`
}

// PartTicket is the presigned URL of one multipart part.
type PartTicket struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

// CompletedPartInput is a part reported by the client on complete.
type CompletedPartInput struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}
