package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Namespace is an opaque tenant inside a project (organization, user, …)
// with its own quota.
type Namespace struct {
	ProjectID          string
	NS                 string
	QuotaBytes         *int64
	QuotaAssets        *int64
	QuotaVideoSecMonth *int64
	UsedBytes          int64
	ReservedBytes      int64
	AssetCount         int64
	QuotaAlertLevel    int
	PurgingAt          *time.Time
	CreatedAt          time.Time
}

const namespaceCols = `project_id, ns, quota_bytes, quota_assets, quota_video_sec_month, used_bytes, reserved_bytes,
	asset_count, quota_alert_level, purging_at, created_at`

func scanNamespace(row interface{ Scan(...any) error }) (*Namespace, error) {
	var n Namespace
	var qb, qa, qv, purging sql.NullInt64
	var created int64
	if err := row.Scan(&n.ProjectID, &n.NS, &qb, &qa, &qv, &n.UsedBytes, &n.ReservedBytes, &n.AssetCount,
		&n.QuotaAlertLevel, &purging, &created); err != nil {
		return nil, mapErr(err)
	}
	n.QuotaBytes, n.QuotaAssets, n.QuotaVideoSecMonth = fromNullInt(qb), fromNullInt(qa), fromNullInt(qv)
	n.PurgingAt = fromNullMs(purging)
	n.CreatedAt = fromMs(created)
	return &n, nil
}

// EnsureNamespace creates the namespace row if it does not exist and returns it.
func (s *Store) EnsureNamespace(ctx context.Context, projectID, ns string, now time.Time) (*Namespace, error) {
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO namespaces (project_id, ns, created_at) VALUES (?, ?, ?) ON CONFLICT(project_id, ns) DO NOTHING`,
		projectID, ns, ms(now)); err != nil {
		return nil, fmt.Errorf("ensure namespace: %w", mapErr(err))
	}
	return scanNamespace(s.w.QueryRowContext(ctx, `SELECT `+namespaceCols+` FROM namespaces WHERE project_id = ? AND ns = ?`, projectID, ns))
}

// GetNamespace returns a namespace.
func (s *Store) GetNamespace(ctx context.Context, projectID, ns string) (*Namespace, error) {
	return scanNamespace(s.r.QueryRowContext(ctx, `SELECT `+namespaceCols+` FROM namespaces WHERE project_id = ? AND ns = ?`, projectID, ns))
}

// SetNamespaceQuota writes the limits of a namespace (creating it if needed).
func (s *Store) SetNamespaceQuota(ctx context.Context, projectID, ns string, bytes, assets, videoSec *int64, now time.Time) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO namespaces (project_id, ns, quota_bytes, quota_assets, quota_video_sec_month, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, ns) DO UPDATE SET quota_bytes = excluded.quota_bytes,
		   quota_assets = excluded.quota_assets, quota_video_sec_month = excluded.quota_video_sec_month`,
		projectID, ns, nullInt(bytes), nullInt(assets), nullInt(videoSec), ms(now))
	return mapErr(err)
}

// SetNamespaceAlertLevel records the last quota alert level.
func (s *Store) SetNamespaceAlertLevel(ctx context.Context, projectID, ns string, level int) error {
	_, err := s.w.ExecContext(ctx, `UPDATE namespaces SET quota_alert_level = ? WHERE project_id = ? AND ns = ?`, level, projectID, ns)
	return mapErr(err)
}

// QuotaDelta is a change to the usage counters of a project and a namespace.
type QuotaDelta struct {
	UsedBytes     int64
	ReservedBytes int64
	AssetCount    int64
}

// AdjustUsage applies d to both the project and the namespace counters.
// Counters never go below zero. The namespace row must exist.
func (s *Store) AdjustUsage(ctx context.Context, projectID, ns string, d QuotaDelta) error {
	if _, err := s.w.ExecContext(ctx,
		`UPDATE projects SET used_bytes = max(0, used_bytes + ?), reserved_bytes = max(0, reserved_bytes + ?) WHERE id = ?`,
		d.UsedBytes, d.ReservedBytes, projectID); err != nil {
		return fmt.Errorf("adjust project usage: %w", mapErr(err))
	}
	if _, err := s.w.ExecContext(ctx,
		`UPDATE namespaces SET used_bytes = max(0, used_bytes + ?), reserved_bytes = max(0, reserved_bytes + ?),
		   asset_count = max(0, asset_count + ?) WHERE project_id = ? AND ns = ?`,
		d.UsedBytes, d.ReservedBytes, d.AssetCount, projectID, ns); err != nil {
		return fmt.Errorf("adjust namespace usage: %w", mapErr(err))
	}
	return nil
}

// ───────────── usage_daily ─────────────

// UsageDelta is added to the usage_daily row of (project, ns, day).
type UsageDelta struct {
	Uploads      int64
	AssetsReady  int64
	AssetsFailed int64
	BytesIn      int64
	BytesStored  int64
	VideoSec     int64
	CPUMillis    int64
	DedupHits    int64
}

// AddUsageDaily upserts the counters of the UTC day containing at.
func (s *Store) AddUsageDaily(ctx context.Context, projectID, ns string, at time.Time, d UsageDelta) error {
	day := at.UTC().Format("2006-01-02")
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO usage_daily (project_id, ns, day, uploads, assets_ready, assets_failed, bytes_in, bytes_stored, video_sec, cpu_ms, dedup_hits)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, ns, day) DO UPDATE SET
		   uploads = uploads + excluded.uploads, assets_ready = assets_ready + excluded.assets_ready,
		   assets_failed = assets_failed + excluded.assets_failed, bytes_in = bytes_in + excluded.bytes_in,
		   bytes_stored = bytes_stored + excluded.bytes_stored, video_sec = video_sec + excluded.video_sec,
		   cpu_ms = cpu_ms + excluded.cpu_ms, dedup_hits = dedup_hits + excluded.dedup_hits`,
		projectID, ns, day, d.Uploads, d.AssetsReady, d.AssetsFailed, d.BytesIn, d.BytesStored, d.VideoSec, d.CPUMillis, d.DedupHits)
	return mapErr(err)
}

// UsageDailyRow is one row of usage_daily.
type UsageDailyRow struct {
	Day string
	UsageDelta
}

// GetUsageDaily returns the usage row of (project, ns, day); zero when absent.
func (s *Store) GetUsageDaily(ctx context.Context, projectID, ns, day string) (UsageDailyRow, error) {
	r := UsageDailyRow{Day: day}
	err := s.r.QueryRowContext(ctx,
		`SELECT uploads, assets_ready, assets_failed, bytes_in, bytes_stored, video_sec, cpu_ms, dedup_hits
		 FROM usage_daily WHERE project_id = ? AND ns = ? AND day = ?`, projectID, ns, day).
		Scan(&r.Uploads, &r.AssetsReady, &r.AssetsFailed, &r.BytesIn, &r.BytesStored, &r.VideoSec, &r.CPUMillis, &r.DedupHits)
	if err == sql.ErrNoRows {
		return r, nil
	}
	return r, mapErr(err)
}

// SumVideoSec returns video seconds of a namespace for days in [fromDay, toDay].
func (s *Store) SumVideoSec(ctx context.Context, projectID, ns, fromDay, toDay string) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx,
		`SELECT coalesce(sum(video_sec), 0) FROM usage_daily WHERE project_id = ? AND ns = ? AND day BETWEEN ? AND ?`,
		projectID, ns, fromDay, toDay).Scan(&n)
	return n, mapErr(err)
}

// ListUsageByDay returns usage summed over namespaces for each day in
// [fromDay, toDay] (YYYY-MM-DD), oldest first. Days without activity are absent.
func (s *Store) ListUsageByDay(ctx context.Context, projectID, fromDay, toDay string) ([]UsageDailyRow, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT day, sum(uploads), sum(assets_ready), sum(assets_failed), sum(bytes_in), sum(bytes_stored), sum(video_sec),
		        sum(cpu_ms), sum(dedup_hits)
		 FROM usage_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day ORDER BY day`, projectID, fromDay, toDay)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []UsageDailyRow
	for rows.Next() {
		var r UsageDailyRow
		if err := rows.Scan(&r.Day, &r.Uploads, &r.AssetsReady, &r.AssetsFailed, &r.BytesIn, &r.BytesStored, &r.VideoSec,
			&r.CPUMillis, &r.DedupHits); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}
