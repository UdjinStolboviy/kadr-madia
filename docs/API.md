# Kadr API v1

The source of truth is `api/openapi.yaml` (OpenAPI 3.1). `@kadr/node`, `@kadr/web` and the admin client are generated from it. This document is an overview.

## General

| | |
|---|---|
| Base URL | `http://kadr.internal:8080/v1` (LAN only) |
| Authentication | `Authorization: Bearer kd_live_…` or `kd_test_…` |
| Format | JSON, `snake_case` fields |
| Idempotency | `Idempotency-Key` on every POST (kept for 24 h) |
| Errors | RFC 9457 `application/problem+json` with a `code` field |
| Pagination | cursor-based: `?limit=50&cursor=…` → `next_cursor` |
| IDs | prefixed ULIDs: `ast_…`, `upl_…`, `blb_…`, `prj_…` |
| Versioning | in the path (`/v1`); adding fields is not a breaking change |

### Key scopes

`uploads:write` · `assets:read` · `assets:write` · `assets:delete` · `urls:sign` · `namespaces:admin` · `imports:write`

## Uploads

### `POST /v1/uploads` — create a ticket

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

**201** — upload required:

```json
{
  "upload_id": "upl_01JD…",
  "method": "PUT",
  "url": "https://<acc>.r2.cloudflarestorage.com/kadr-private/tmp/acme/upl_01JD…?X-Amz-…",
  "headers": { "Content-Type": "image/jpeg", "Content-Length": "384211" },
  "expires_at": "2026-11-16T21:15:00Z"
}
```

For `size` > 64 MB: `"method": "MULTIPART"`, `"part_size": 16777216`, `"parts": [{ "number": 1, "url": "…" }, …]` (first 20 parts; the rest via `POST /v1/uploads/{id}/parts`).

**200** — deduplicated (`sha256` already known within `project + visibility + preset_version`): returns the ready `asset` immediately, no upload needed.

**Errors:** `quota_exceeded` (402), `unsupported_media` (415), `too_large` (413), `preset_not_found` (404).

### `POST /v1/uploads/{id}/parts` — get more part URLs

`{ "part_numbers": [21, 22, 23] }` → `{ "parts": [...] }`

### `POST /v1/uploads/{id}/complete?wait=5s`

Body for multipart: `{ "parts": [{ "number": 1, "etag": "…" }, …] }`.

- Returns the `asset`. If processing finishes within `wait` (typical for photos) — `status: "ready"`, otherwise `status: "processing"` followed later by an `asset.ready` webhook.
- Idempotent.

### `DELETE /v1/uploads/{id}` — cancel

Aborts the multipart upload, deletes the tmp object and releases the quota reservation.

### `POST /v1/assets` — server-side upload (multipart/form-data)

For backend processes (receipt scanner, imports): files ≤ 25 MB go to Kadr directly over the LAN, bypassing R2 tmp.

### `POST /v1/imports/url`

`{ "url": "https://…", "preset": "photo-lite", "namespace": "org_8f2c", "visibility": "public" }` → `202 { asset (processing) }`

### `POST /v1/imports/cloud`

`{ "connection_id": "con_…", "file_id": "…", "preset": "…", "namespace": "…", "visibility": "…" }` → `202`

## Assets

### The `asset` object

```json
{
  "id": "ast_01JD…",
  "status": "ready",
  "kind": "image",
  "namespace": "org_8f2c",
  "visibility": "public",
  "preset": "photo-lite",
  "preset_version": 3,
  "original": { "filename": "IMG_2231.HEIC", "content_type": "image/heic", "bytes": 4382110, "width": 4032, "height": 3024 },
  "renditions": [
    { "name": "main", "format": "avif", "bytes": 19422, "width": 800, "height": 600, "url": "https://cdn.example.com/acme/blb_01JD…/main.avif" },
    { "name": "main", "format": "webp", "bytes": 20110, "width": 800, "height": 600, "url": "https://cdn.example.com/acme/blb_01JD…/main.webp" }
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

For `visibility: "private"` there is no `url`; instead there is a `key`, signed via the SDK or `POST /v1/signed-urls`.

| Method | Path | Description |
|---|---|---|
| GET | `/v1/assets/{id}` | Get |
| GET | `/v1/assets?namespace=&kind=&status=&cursor=` | List |
| PATCH | `/v1/assets/{id}` | `metadata`, `visibility` (triggers a cross-bucket copy) |
| DELETE | `/v1/assets/{id}` | Delete (R2 objects are removed by refcount GC) |
| POST | `/v1/assets/{id}/reprocess` | Reprocess with another/new preset version |

## Signed URLs (private files)

### Locally in the SDK (recommended, no network)

```ts
const url = kadr.signUrl(asset.renditions[0].key, { ttl: 3600, download: "receipt.avif" });
// https://media.example.com/acme/blb_…/main.avif?exp=1763330400&kid=k3&sig=Base64Url(HMAC-SHA256(secret, "GET\n/acme/blb_…/main.avif\n1763330400\nreceipt.avif"))
```

### Via the API

`POST /v1/signed-urls` `{ "items": [{ "asset_id": "ast_…", "rendition": "main", "ttl": 3600 }] }` → `{ "items": [{ "url": "…", "expires_at": "…" }] }`

## Namespaces and quotas

| Method | Path | Description |
|---|---|---|
| GET | `/v1/namespaces/{ns}/usage` | Bytes, count, video minutes this month, limits |
| PUT | `/v1/namespaces/{ns}/quota` | Set limits (the organization's plan in the product) |
| DELETE | `/v1/namespaces/{ns}` | Delete all namespace data (GDPR), async, `namespace.purged` event |

## Presets

`GET /v1/presets` · `GET /v1/presets/{slug}` (managed via the admin console or `PUT /v1/presets/{slug}` with the `namespaces:admin` scope).

## Webhooks

Format: [Standard Webhooks](https://www.standardwebhooks.com/) — headers `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256>`.

```json
{ "type": "asset.ready", "timestamp": "2026-11-16T20:59:13Z", "data": { "asset": { … }, "metadata": { … } } }
```

| Event | When |
|---|---|
| `asset.ready` | Processing finished |
| `asset.failed` | Error (`error.code`: `unsupported_media`, `too_long`, `corrupt`, `timeout`, …) |
| `asset.updated` | Visibility or metadata changed |
| `asset.deleted` | Deleted |
| `upload.expired` | Ticket expired without a file |
| `quota.warning` | Namespace reached ≥ 80% |
| `quota.exceeded` | Limit reached |
| `namespace.purged` | Namespace deletion finished |

Retries: 10 s, 30 s, 2 min, 10 min, 30 min, 1 h, 3 h, 6 h, 12 h (~24 h total), then `dead` + manual re-send from the admin console. Receivers must be idempotent by `webhook-id`.

## Admin API

`/admin/api/*` on a separate port, cookie session (`HttpOnly`, `Secure`, `SameSite=Strict`) + CSRF token. See [ADMIN_API.md](ADMIN_API.md). Not a public contract.
