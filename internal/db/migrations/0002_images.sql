-- Kadr M1 (images): columns and indexes needed by the upload pipeline, the
-- queue, quotas and webhooks. Additive only.

-- An asset remembers the preset version it was (or is being) processed with,
-- even before a Blob exists (processing/failed assets have no blob_id).
ALTER TABLE assets ADD COLUMN preset_id TEXT REFERENCES presets(id);
ALTER TABLE assets ADD COLUMN preset_version INTEGER;

-- Last quota alert emitted for the namespace: 0 = none, 1 = quota.warning
-- (>= 80%), 2 = quota.exceeded (>= 100%). Lowered again when usage drops, so
-- each crossing emits exactly one event.
ALTER TABLE namespaces ADD COLUMN quota_alert_level INTEGER NOT NULL DEFAULT 0;

-- GET /v1/assets without a namespace filter (cursor = created_at, id).
CREATE INDEX assets_project_created ON assets(project_id, created_at DESC, id DESC);
CREATE INDEX assets_upload ON assets(upload_id);

-- Retention cleanup (DATA_MODEL.md): finished jobs, idempotency keys.
CREATE INDEX jobs_finished ON jobs(status, finished_at) WHERE finished_at IS NOT NULL;
CREATE INDEX idempotency_keys_created ON idempotency_keys(created_at);

-- Admin delivery log per endpoint.
CREATE INDEX webhook_deliveries_endpoint ON webhook_deliveries(endpoint_id);
CREATE INDEX webhook_endpoints_project ON webhook_endpoints(project_id);
