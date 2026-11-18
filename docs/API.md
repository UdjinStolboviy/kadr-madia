# Kadr API v1

The source of truth is `api/openapi.yaml` (OpenAPI 3.1), kept in sync with the implementation in `internal/api/public` and `internal/pipeline`. The types of `@kadr/node` are generated from it (`sdk/node`, `pnpm gen`). This document is an overview; where it is less precise than the spec, the spec wins.

## General

| | |
|---|---|
| Base URL | `http://kadr.internal:8080/v1` (LAN only) |
| Authentication | `Authorization: Bearer kd_live_…` or `kd_test_…` |
| Format | JSON, `snake_case` fields, RFC 3339 UTC timestamps |
| Idempotency | `Idempotency-Key` on every POST (kept for 24 h), see below |
| Errors | RFC 9457 `application/problem+json` with a `code` field, see [Errors](#errors) |
| Pagination | cursor-based: `?limit=50&cursor=…` → `{ "items": [...], "next_cursor": "…" \| null }` |
| IDs | prefixed ULIDs: `ast_…`, `upl_…`, `blb_…`, `evt_…`, `prj_…` |
| Versioning | in the path (`/v1`); adding fields is not a breaking change |
| Not yet implemented | any other `/v1` path or method answers `501 not_implemented` |

### Key scopes

`uploads:write` · `assets:read` · `assets:write` · `assets:delete` · `urls:sign` · `namespaces:admin` · `imports:write`

`urls:sign` and `imports:write` can be granted but no endpoint uses them yet. Presets are readable with any valid key.

### Idempotency

- Applies to POST requests with an `Idempotency-Key` header (1–255 printable ASCII characters; otherwise `400 invalid_idempotency_key`).
- Only `2xx` responses are stored and replayed, with the header `Idempotent-Replayed: true`. Any other outcome releases the key, so the request can be retried with the same key.
- Same key while the first request is still running → `409 idempotency_in_progress`.
- Same key with a different method, path, query or body → `422 idempotency_mismatch`.
- The request hash covers the raw body bytes: a retried multipart request must resend the identical body (same boundary).

### `GET /v1/ping`

Verifies a key: `200 { "ok": true, "project": "acme", "project_id": "prj_…", "environment": "live" | "test", "key_id": "…", "scopes": [...] }`.

## Uploads

### `POST /v1/uploads` — create a ticket

Scope `uploads:write`.

```json
{
  "preset": "photo-lite",
  "namespace": "org_8f2c",
  "visibility": "private",
  "filename": "receipt.jpg",
  "content_type": "image/jpeg",
  "size": 384211,
  "sha256": "9f86d0…",
  "metadata": { "entity": "receipt", "entity_id": "rcp_123" }
}
```

`preset`, `namespace`, `visibility`, `content_type` and `size` (exact bytes) are required. `namespace`: 1–128 characters of `[A-Za-z0-9._:@-]`, starting with a letter or digit. `metadata`: a JSON object of at most 16 KB. `content_type` must map to a format accepted by the preset (`limits.accept`); only image presets can be used in this version.

**201** — upload required. The client PUTs the file to `url` with exactly `headers` (Content-Type and Content-Length are signed); the ticket is valid for 15 minutes and reserves `size` bytes of the project and namespace quota.

```json
{
  "upload_id": "upl_01jd…",
  "method": "PUT",
  "url": "https://<acc>.r2.cloudflarestorage.com/kadr-private/tmp/acme/upl_01jd…?X-Amz-…",
  "headers": { "Content-Type": "image/jpeg", "Content-Length": "384211" },
  "expires_at": "2026-11-16T21:15:00Z"
}
```

Image files are limited to 64 MB. **Video files over 64 MB** get a multipart ticket (`"method": "MULTIPART"`) instead of `url`/`headers`:

```json
{ "upload_id": "upl_…", "method": "MULTIPART", "part_size": 16777216,
  "parts": [{ "part_number": 1, "url": "https://…" }, { "part_number": 2, "url": "https://…" }],
  "expires_at": "2026-11-16T22:00:00Z" }
```

`PUT` bytes `[(n-1)*part_size, n*part_size)` of the file to each part URL (the last part may be smaller; parts can be uploaded in parallel and retried), collect the `ETag` response header of every part, then `POST /v1/uploads/{id}/complete` with `{ "parts": [{ "part_number": 1, "etag": "…" }, …] }`. Complete is idempotent. Multipart tickets live for 1 hour; abandoned ones are aborted by the sweeper. The bucket CORS rule must expose the `ETag` header for browser uploads (`@kadr/web` reports a clear error otherwise).

**200** — deduplicated: `sha256` matches a ready blob within the same project, visibility and preset version. A new ready asset referencing it is created (and `asset.ready` is emitted); no upload is needed:

```json
{ "asset": { "id": "ast_…", "status": "ready", … } }
```

**Errors:** `validation_failed` (422), `preset_not_found` (404), `unsupported_media` (415), `too_large` (413), `quota_exceeded` (402), `storage_not_configured` (409).

### `POST /v1/uploads/{id}/complete?wait=5s`

Scope `uploads:write`. No body.

- Verifies the uploaded object (exact size), creates the asset and enqueues processing.
- `wait`: a duration (`5s`, `1500ms`) or seconds (`5`); default `0`, capped at `30s`. If processing finishes within `wait` (typical for photos) the asset is returned with `status: "ready"` (or `"failed"`), otherwise with `status: "processing"`, followed by an `asset.ready` / `asset.failed` webhook.
- Idempotent: repeated calls return the same asset.

**Errors:** `upload_incomplete` (409, the file has not been PUT yet), `upload_aborted` (409), `upload_not_pending` (409), `upload_expired` (410), `size_mismatch` (422 — the object size differs from the ticket; the upload is failed and its reservation released), `storage_error` (502), `not_found` (404).

### `DELETE /v1/uploads/{id}` — cancel

Scope `uploads:write`. Deletes the temporary object and releases the quota reservation: `204`. Cancelling twice is a no-op; `409 upload_not_pending` once `complete` was called.

### `POST /v1/assets` — server-side upload (multipart/form-data)

Scope `uploads:write`. For backend processes (scanners, imports): files ≤ 25 MB go to Kadr directly over the LAN, are staged on local disk and processed through the same queue as ticket uploads. Parts may come in any order:

| Field | |
|---|---|
| `preset`, `namespace`, `visibility` | required, as for tickets |
| `metadata` | JSON object serialized as a string (≤ 16 KB) |
| `filename` | overrides the file name of the `file` part |
| `file` | the file (exactly one) |

`?wait=` works as for `complete`. The server hashes the file; deduplicated content returns a ready asset immediately. Response: **201** with the asset.

**Errors:** as for tickets, plus `too_large` (413, over 25 MB), `unsupported_content_type` (415, the body is not multipart/form-data), `invalid_multipart` (400).

### Documents

PDF, DOCX, XLSX, PPTX, ODT, ODS, ODP, RTF, TXT and CSV are accepted as assets of `kind: "file"` (see [PIPELINES.md §4](PIPELINES.md)). Use `"mode"` or a `file-*` preset (`file-original` stores as uploaded). The format is verified from the content; a mismatch fails the asset with `unsupported_media` / `corrupt`. The asset has a `main` rendition whose `format` is the document format (`pdf`, `docx`, …) and, for PDFs, a `thumb` rendition (avif + webp) plus `placeholder`. Documents are stored with `Content-Disposition: attachment`. Private documents are delivered with signed URLs (`rendition: "main"`, optional `download` file name).

### Compression modes

Instead of `preset` any upload or import may send `"mode": "compact" | "balanced" | "quality"` (see [PIPELINES.md §2b](PIPELINES.md)). The preset becomes `image-<mode>`, `video-<mode>` or `file-<mode>` (documents) depending on the content type (or the file name/URL extension for server-side uploads and imports). Sending both `preset` and `mode` is a `422`. With neither, the project's default mode is used. The asset's `preset` field reports the resulting preset.

### `POST /v1/imports/url` — import from a URL

`{ "url": "https://…", "preset": "photo-lite", "namespace": "org_8f2c", "visibility": "public", "filename"?, "metadata"? }` → `202` with the asset in `processing`; `asset.ready` / `asset.failed` follow. Scope `imports:write`.

The fetch is SSRF-safe: http/https only, no credentials in the URL, ports 80/443/8080/8443, the host is resolved once and **every** address must be public (RFC 1918, loopback, link-local, CGNAT, cloud metadata, multicast and IPv6 ULA are refused), the connection goes to the verified IP, at most 5 redirects, each re-validated. Size is capped by the preset's `maxInputBytes`. Failure codes: `import_blocked`, `import_failed` (HTTP errors, timeouts after retries), `too_large`, plus the engine codes.

### `POST /v1/imports/cloud` — import from a cloud drive

`{ "connection_id": "con_…", "file_id": "…", "preset", "namespace", "visibility" }` → `202`. Connections (Google Drive, OneDrive, Dropbox) are created in the admin console from an `rclone authorize` token; `file_id` is the Drive file id, or a path for OneDrive/Dropbox. Requires the `rclone` binary on the host.

## Assets

### The `asset` object

```json
{
  "id": "ast_01jd…",
  "status": "ready",
  "kind": "image",
  "namespace": "org_8f2c",
  "visibility": "public",
  "preset": "photo-lite",
  "preset_version": 3,
  "original": { "filename": "IMG_2231.HEIC", "content_type": "image/heic", "bytes": 4382110, "width": 4032, "height": 3024 },
  "renditions": [
    { "name": "main", "format": "avif", "bytes": 19422, "width": 800, "height": 600, "url": "https://cdn.example.com/acme/blb_01jd…/main.avif" },
    { "name": "main", "format": "webp", "bytes": 20110, "width": 800, "height": 600, "url": "https://cdn.example.com/acme/blb_01jd…/main.webp" }
  ],
  "placeholder": { "thumbhash": "1QcSHQRnh493V4dIh4eXh1h4kJUI", "dominant_color": "#a3866c" },
  "duration_sec": null,
  "poster": null,
  "metadata": { "entity": "listing", "entity_id": "lst_77" },
  "savings_ratio": 0.991,
  "created_at": "2026-11-16T20:59:12Z",
  "ready_at": "2026-11-16T20:59:13Z"
}
```

- `renditions[].encoder` (video renditions): the encoder that produced the file: `h264_vaapi` or `libx264` (H.264; `libx264` on a VAAPI host means the hardware encode fell back to software) or `libsvtav1` (AV1 presets, see PIPELINES.md).
- `status`: `processing` → `ready` | `failed`. `deleted` appears only in `asset.deleted` webhook payloads.
- `kind`: `image` or `video` (only image presets are processed in this version).
- `original.filename`, `original.content_type`, `width`, `height`, `placeholder` (and its fields), `duration_sec`, `poster`, `savings_ratio` and `ready_at` may be `null`. `content_type` is the declared type until processing detects the real one.
- `renditions` is empty unless the asset is `ready`.
- `error`: present only on failed assets — `{ "code": "corrupt", "detail": "…" }`, codes as in [`asset.failed`](#webhooks).
- For `visibility: "private"` renditions carry no `url`; instead they carry a `key`, to be signed with the SDK (`kadr.signUrl`).

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/v1/assets/{id}` | `assets:read` | Get (deleted assets: `404 not_found`) |
| GET | `/v1/assets?namespace=&kind=&status=&limit=&cursor=` | `assets:read` | List, newest first. `status`: `processing`, `ready` or `failed`; `limit` 1–200 (default 50). Returns `{ "items": [...], "next_cursor": … }` |
| PATCH | `/v1/assets/{id}` | `assets:write` | `{ "metadata": {…} }` replaces the metadata and emits `asset.updated`. Changing `visibility` copies the renditions server-side into the other bucket (new blob, public copies get public cache headers), re-points the asset and lets GC remove the old objects; it can be combined with `metadata`, emits one `asset.updated` (`changes: ["visibility", …]`) and answers `409 asset_not_ready` unless the asset is ready. Sending the current visibility is a no-op |
| DELETE | `/v1/assets/{id}` | `assets:delete` | `204`. Soft delete, releases quota, emits `asset.deleted`; stored files are removed by refcount GC. `409 asset_processing` while the asset is processing |
| POST | `/v1/assets/{id}/reprocess` | | Planned (`501`) |

## Signed URLs (private files)

### Locally in the SDK (recommended, no network)

```ts
const url = kadr.signUrl(asset.renditions[0].key, { ttl: 3600, download: "receipt.avif" });
// https://media.example.com/acme/blb_…/main.avif?exp=1763330400&kid=k3&sig=Base64Url(HMAC-SHA256(secret, "GET\n/acme/blb_…/main.avif\n1763330400\nreceipt.avif"))
```

The canonical form is specified in `sdk/node/README.md`. The `kadr-edge` Worker (`edge/kadr-edge`) verifies these URLs. Signing keys are created and rotated per project in the admin console (Signing keys tab) and published to the Worker with `POST /admin/api/signing-keys/publish` (or copied into the `SIGNING_KEYS` Worker secret).

### Via the API (scope `urls:sign`)

`POST /v1/signed-urls` `{ "items": [{ "asset_id": "ast_…", "rendition": "main.avif", "ttl": 3600, "download": "receipt.avif" }] }` → `{ "items": [{ "url": "…", "expires_at": "…" }] }` (1–100 items, ttl ≤ 7 days). Only ready **private** assets can be signed; `409 no_signing_key` until a key exists, `503 signing_unavailable` without a private delivery host: the project's storage profile `private_base_url` wins, `KADR_EDGE_BASE_URL` is the default.

## Namespaces and quotas

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/v1/namespaces/{ns}/usage` | `assets:read` or `namespaces:admin` | Usage and limits (unknown namespaces report zeros) |
| PUT | `/v1/namespaces/{ns}/quota` | `namespaces:admin` | Replace all limits; returns the usage |
| DELETE | `/v1/namespaces/{ns}` | | Planned (`501`): delete all namespace data (GDPR), async, `namespace.purged` event |

Usage:

```json
{
  "namespace": "org_8f2c",
  "used_bytes": 1834221,
  "reserved_bytes": 0,
  "asset_count": 42,
  "video_sec_month": 0,
  "quota_bytes": 10737418240,
  "quota_assets": null,
  "quota_video_sec_month": null
}
```

`used_bytes` counts the stored renditions of the namespace's assets (a deduplicated asset counts its blob again); `reserved_bytes` is the declared size of uploads in flight; `video_sec_month` covers the current calendar month (UTC). `null` limits mean unlimited.

Quota body: `{ "quota_bytes": 10737418240, "quota_assets": null, "quota_video_sec_month": null }` — every field is replaced; an omitted field is stored as `null`. Values must be `>= 0`. Exceeding a project or namespace limit makes uploads fail with `402 quota_exceeded`.

## Presets

`GET /v1/presets` → `{ "items": [Preset] }` · `GET /v1/presets/{slug}` → `Preset` (`404 preset_not_found`). Any valid key.

`Preset`: `{ "slug": "photo-lite", "kind": "image", "description": "…", "version": 3, "params": { … }, "created_at": "…" }` — `params` are the parameters of the current version (docs/PIPELINES.md §1).

Presets are managed in the admin console; `PUT /v1/presets/{slug}` is planned (`501`).

## Webhooks

Format: [Standard Webhooks](https://www.standardwebhooks.com/) — headers `webhook-id` (the event id `evt_…`, identical across retries), `webhook-timestamp` (unix seconds of the attempt), `webhook-signature: v1,<base64 HMAC-SHA256>` over `{webhook-id}.{webhook-timestamp}.{raw body}`, keyed with the base64-decoded part of the endpoint secret after `whsec_`.

Body: `{ "type": "…", "timestamp": "…", "data": { … } }`.

| Event | When | `data` |
|---|---|---|
| `asset.ready` | Processing finished, or a deduplicated asset was created | `{ asset, metadata }` |
| `asset.failed` | Processing failed | `{ asset, metadata, error: { code, detail? } }` |
| `asset.updated` | Metadata changed (later also visibility) | `{ asset, metadata, changes: ["metadata"] }` |
| `asset.deleted` | Deleted | `{ asset, metadata }`, `asset.status` is `deleted` |
| `asset.moderated` | A human or AI agent recorded a moderation decision in the admin ([AGENTS.md](AGENTS.md#moderation)) | `{ asset_id, namespace, status: approved\|flagged\|rejected, previous_status, labels, note, actor_type }` |
| `upload.expired` | Ticket expired without a file | `{ upload_id, namespace, preset, visibility, filename, expires_at, metadata }` |
| `quota.warning` | Namespace byte usage crossed 80 % of its `quota_bytes` | `{ namespace, used_bytes, quota_bytes, ratio }` |
| `quota.exceeded` | Namespace byte usage reached 100 % | same as `quota.warning` |
| `webhook.test` | "Send test" in the admin console (ignores the endpoint's filter) | `{ endpoint_id, message }` |

- `error.code` of `asset.failed`: `unsupported_media`, `too_large`, `too_many_pixels`, `corrupt`, `timeout`, `engine_failure` (from the image engine) and `source_missing`, `checksum_mismatch`, `internal` (from the pipeline).
- Quota events are emitted per namespace only (not for the project quota), once per upward crossing; the level resets when usage drops.
- `namespace.purged` can be subscribed to but is not emitted until namespace deletion exists.

Retries: 10 s, 30 s, 2 min, 10 min, 30 min, 1 h, 3 h, 6 h, 12 h (~24 h total), then `dead` + manual re-send from the admin console. Receivers must answer `2xx` within 10 s and be idempotent by `webhook-id`.

## Errors

`application/problem+json`:

```json
{ "type": "about:blank", "title": "Unprocessable Entity", "status": 422, "code": "validation_failed",
  "detail": "request validation failed", "errors": { "size": "required; the exact file size in bytes" } }
```

`errors` (field → message) is present only for `validation_failed`.

| Status | Codes |
|---|---|
| 400 | `invalid_json`, `invalid_multipart`, `invalid_idempotency_key`, `invalid_body` |
| 401 | `unauthenticated` |
| 402 | `quota_exceeded` |
| 403 | `insufficient_scope`, `project_disabled` |
| 404 | `not_found`, `preset_not_found` |
| 409 | `upload_incomplete`, `upload_aborted`, `upload_not_pending`, `asset_processing`, `storage_not_configured`, `idempotency_in_progress` |
| 410 | `upload_expired` |
| 413 | `too_large` |
| 415 | `unsupported_media`, `unsupported_content_type` |
| 422 | `validation_failed`, `size_mismatch`, `idempotency_mismatch` |
| 500 | `internal` |
| 501 | `not_implemented` |
| 502 | `storage_error` |

## Admin API

`/admin/api/*` on a separate port, cookie session (`HttpOnly`, `Secure`, `SameSite=Strict`) + CSRF token. See [ADMIN_API.md](ADMIN_API.md). Not a public contract.
