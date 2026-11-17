package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

func (s *Service) loadAsset(ctx context.Context, projectID, id string) (*db.Asset, error) {
	a, err := s.store.GetAsset(ctx, id)
	if errors.Is(err, db.ErrNotFound) || (err == nil && (a.ProjectID != projectID || a.Status == db.AssetDeleted)) {
		return nil, apiErr(http.StatusNotFound, CodeNotFound, "asset %s not found", id)
	}
	return a, err
}

// GetAsset returns one asset of the project.
func (s *Service) GetAsset(ctx context.Context, projectID, id string) (*AssetView, error) {
	a, err := s.loadAsset(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	return s.assetView(ctx, s.store, a)
}

// ListAssetsInput filters GET /v1/assets.
type ListAssetsInput struct {
	Namespace string
	Kind      string
	Status    string
	Cursor    string
	Limit     int
}

// AssetPage is one page of assets.
type AssetPage struct {
	Items      []*AssetView `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// ListAssets returns assets newest first with cursor pagination.
func (s *Service) ListAssets(ctx context.Context, projectID string, in ListAssetsInput) (*AssetPage, error) {
	errs := map[string]string{}
	if in.Namespace != "" && !ValidNamespace(in.Namespace) {
		errs["namespace"] = "invalid namespace"
	}
	if in.Kind != "" && in.Kind != "image" && in.Kind != "video" {
		errs["kind"] = `must be "image" or "video"`
	}
	switch in.Status {
	case "", db.AssetProcessing, db.AssetReady, db.AssetFailed:
	default:
		errs["status"] = `must be "processing", "ready" or "failed"`
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 200 {
		errs["limit"] = "must be between 1 and 200"
	}
	f := db.AssetFilter{ProjectID: projectID, NS: in.Namespace, Kind: in.Kind, Status: in.Status, Limit: in.Limit + 1}
	if in.Cursor != "" {
		created, id, ok := decodeCursor(in.Cursor)
		if !ok {
			errs["cursor"] = "invalid cursor"
		}
		f.CursorCreated, f.CursorID = created, id
	}
	if len(errs) > 0 {
		return nil, validationErr(errs)
	}
	rows, err := s.store.ListAssets(ctx, f)
	if err != nil {
		return nil, err
	}
	page := &AssetPage{Items: []*AssetView{}}
	if len(rows) > in.Limit {
		last := rows[in.Limit-1]
		c := encodeCursor(db.AssetCreatedMs(last), last.ID)
		page.NextCursor = &c
		rows = rows[:in.Limit]
	}
	for _, a := range rows {
		v, err := s.assetView(ctx, s.store, a)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, v)
	}
	return page, nil
}

func encodeCursor(createdMs int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdMs, 10) + ":" + id))
}

func decodeCursor(c string) (int64, string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, "", false
	}
	ts, id, ok := strings.Cut(string(b), ":")
	n, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil || id == "" {
		return 0, "", false
	}
	return n, id, true
}

// PatchAssetInput is the body of PATCH /v1/assets/{id}. Raw fields keep
// "absent" distinguishable from null.
type PatchAssetInput struct {
	Metadata   json.RawMessage `json:"metadata"`
	Visibility *string         `json:"visibility"`
}

// PatchAsset replaces metadata and/or changes visibility (a copy of the
// renditions into the other bucket); repeating the current visibility is accepted.
func (s *Service) PatchAsset(ctx context.Context, projectID, id string, in PatchAssetInput) (*AssetView, error) {
	a, err := s.loadAsset(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if in.Visibility != nil && *in.Visibility != a.Visibility {
		if *in.Visibility != "public" && *in.Visibility != "private" {
			return nil, validationErr(map[string]string{"visibility": `must be "public" or "private"`})
		}
		return s.changeVisibility(ctx, a, *in.Visibility, in.Metadata)
	}
	if in.Metadata == nil {
		return s.assetView(ctx, s.store, a)
	}
	meta, msg := normalizeMetadata(in.Metadata)
	if msg != "" {
		return nil, validationErr(map[string]string{"metadata": msg})
	}
	var view *AssetView
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.SetAssetMetadata(ctx, a.ID, meta); err != nil {
			return err
		}
		cur, err := tx.GetAsset(ctx, a.ID)
		if err != nil {
			return err
		}
		if view, err = s.assetView(ctx, tx, cur); err != nil {
			return err
		}
		return s.emit(ctx, tx, projectID, webhook.EventAssetUpdated, map[string]any{
			"asset": view, "metadata": view.Metadata, "changes": []string{"metadata"},
		})
	})
	if err != nil {
		return nil, err
	}
	s.afterCommit("")
	return view, nil
}

// DeleteAsset soft-deletes an asset, releases its blob reference and quota
// and emits asset.deleted. A blob whose refcount drops to zero becomes
// gc_pending and a gc_blob job is enqueued.
func (s *Service) DeleteAsset(ctx context.Context, projectID, id string) error {
	a, err := s.loadAsset(ctx, projectID, id)
	if err != nil {
		return err
	}
	if a.Status == db.AssetProcessing {
		return apiErr(http.StatusConflict, CodeAssetProcessing, "asset %s is still processing; delete it once it is ready or failed", a.ID)
	}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		view, err := s.assetView(ctx, tx, a)
		if err != nil {
			return err
		}
		ok, err := tx.MarkAssetDeleted(ctx, a.ID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return apiErr(http.StatusNotFound, CodeNotFound, "asset %s not found", a.ID)
		}
		if a.BlobID != nil {
			b, err := tx.ReleaseBlobRef(ctx, *a.BlobID)
			if err != nil {
				return err
			}
			if err := tx.AdjustUsage(ctx, a.ProjectID, a.NS, db.QuotaDelta{UsedBytes: -b.TotalBytes, AssetCount: -1}); err != nil {
				return err
			}
			if b.Status == db.BlobGCPending {
				if _, _, err := s.queue.Enqueue(ctx, tx, queue.EnqueueOptions{
					Kind: JobGCBlob, Pool: queue.PoolMaintenance, DedupeKey: "gc:" + b.ID, Payload: gcPayload{BlobID: b.ID},
				}); err != nil {
					return err
				}
			}
			if _, err := tx.EnsureNamespace(ctx, a.ProjectID, a.NS, s.now()); err != nil {
				return err
			}
			if err := s.updateQuotaAlerts(ctx, tx, a.ProjectID, a.NS); err != nil {
				return err
			}
		}
		view.Status = db.AssetDeleted
		return s.emit(ctx, tx, projectID, webhook.EventAssetDeleted, map[string]any{"asset": view, "metadata": view.Metadata})
	})
	if err != nil {
		return err
	}
	s.afterCommit(a.ID)
	return nil
}

// ───────────── presets and namespaces ─────────────

// ListPresets returns the project's active presets.
func (s *Service) ListPresets(ctx context.Context, projectID string) ([]PresetView, error) {
	ps, err := s.store.ListPresets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]PresetView, 0, len(ps))
	for _, p := range ps {
		out = append(out, presetView(p))
	}
	return out, nil
}

// GetPreset returns one active preset by slug.
func (s *Service) GetPreset(ctx context.Context, projectID, slug string) (*PresetView, error) {
	p, err := s.store.GetPresetBySlug(ctx, projectID, slug)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apiErr(http.StatusNotFound, CodePresetNotFound, "preset %q does not exist in this project", slug)
	}
	if err != nil {
		return nil, err
	}
	v := presetView(p)
	return &v, nil
}

// NamespaceUsage returns usage and limits; unknown namespaces report zeros.
func (s *Service) NamespaceUsage(ctx context.Context, projectID, ns string) (*UsageView, error) {
	if !ValidNamespace(ns) {
		return nil, validationErr(map[string]string{"namespace": "invalid namespace"})
	}
	v := &UsageView{Namespace: ns}
	n, err := s.store.GetNamespace(ctx, projectID, ns)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return v, nil
	case err != nil:
		return nil, err
	}
	v.UsedBytes, v.ReservedBytes, v.AssetCount = n.UsedBytes, n.ReservedBytes, n.AssetCount
	v.QuotaBytes, v.QuotaAssets, v.QuotaVideoSecMonth = n.QuotaBytes, n.QuotaAssets, n.QuotaVideoSecMonth
	now := s.now().UTC()
	first := now.AddDate(0, 0, -now.Day()+1).Format("2006-01-02")
	if v.VideoSecMonth, err = s.store.SumVideoSec(ctx, projectID, ns, first, now.Format("2006-01-02")); err != nil {
		return nil, err
	}
	return v, nil
}

// QuotaInput is the body of PUT /v1/namespaces/{ns}/quota. null = unlimited.
type QuotaInput struct {
	QuotaBytes         *int64 `json:"quota_bytes"`
	QuotaAssets        *int64 `json:"quota_assets"`
	QuotaVideoSecMonth *int64 `json:"quota_video_sec_month"`
}

// SetNamespaceQuota replaces the limits of a namespace.
func (s *Service) SetNamespaceQuota(ctx context.Context, projectID, ns string, in QuotaInput) (*UsageView, error) {
	errs := map[string]string{}
	if !ValidNamespace(ns) {
		errs["namespace"] = "invalid namespace"
	}
	for name, v := range map[string]*int64{"quota_bytes": in.QuotaBytes, "quota_assets": in.QuotaAssets, "quota_video_sec_month": in.QuotaVideoSecMonth} {
		if v != nil && *v < 0 {
			errs[name] = "must be >= 0 or null"
		}
	}
	if len(errs) > 0 {
		return nil, validationErr(errs)
	}
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.SetNamespaceQuota(ctx, projectID, ns, in.QuotaBytes, in.QuotaAssets, in.QuotaVideoSecMonth, s.now()); err != nil {
			return err
		}
		return s.updateQuotaAlerts(ctx, tx, projectID, ns)
	})
	if err != nil {
		return nil, err
	}
	s.afterCommit("")
	return s.NamespaceUsage(ctx, projectID, ns)
}
