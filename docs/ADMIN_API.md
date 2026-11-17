# Admin API (M0)

Internal contract between the Kadr backend and the embedded admin console (`web/`). Not a public API.

## Transport

- The admin server listens on `KADR_ADMIN_ADDR` (default `127.0.0.1:8081`).
- `GET /` and any unknown path outside `/admin/api` → SPA (`index.html` from `web/dist`, embedded via `go:embed`). Static `/assets/*` are served with `Cache-Control: public, max-age=31536000, immutable`.
- The API lives under `/admin/api`. JSON, `snake_case` fields, timestamps as RFC 3339 strings (UTC).
- Errors use `application/problem+json`: `{ "type": "about:blank", "title": "…", "status": 400, "code": "validation_failed", "detail": "…", "errors": { "field": "message" } }`.
- Session: cookie `__Host-kadr_session` (`HttpOnly; Secure; SameSite=Strict; Path=/`). In dev mode (`KADR_DEV=1`) the cookie is `kadr_session` without `Secure`.
- CSRF: every POST/PUT/PATCH/DELETE request (except `POST /auth/login`) carries an `X-CSRF-Token` header whose value comes from the `login` or `me` response.
- Unauthenticated request → `401 { code: "unauthenticated" }`. CSRF failure → `403 { code: "csrf_failed" }`.

## Auth

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/admin/api/auth/login` | `{ email, password, totp_code? }` | `200 { admin, csrf_token }` · `401 invalid_credentials` · `401 totp_required` (TOTP is enabled but no code was sent) |
| POST | `/admin/api/auth/logout` | — | `204` |
| GET | `/admin/api/auth/me` | — | `200 { admin, csrf_token }` |
| POST | `/admin/api/auth/totp/setup` | — | `200 { secret, otpauth_url }` |
| POST | `/admin/api/auth/totp/enable` | `{ code }` | `204` · `400 invalid_code` |
| POST | `/admin/api/auth/totp/disable` | `{ password }` | `204` |

`admin`: `{ id, email, role, totp_enabled, created_at, last_login_at }`

Login rate limit: 5 attempts / min per IP and email → `429 rate_limited`.

## Projects

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/admin/api/projects` | — | `200 { items: Project[] }` |
| POST | `/admin/api/projects` | `{ slug, name, environment, storage_profile_id?, quota_bytes?, allowed_origins? }` | `201 Project` (seeds the default presets) · `409 slug_taken` |
| GET | `/admin/api/projects/{id}` | — | `200 Project` |
| PATCH | `/admin/api/projects/{id}` | `{ name?, storage_profile_id?, quota_bytes?, allowed_origins?, disabled? }` | `200 Project` |

`Project`: `{ id, slug, name, environment: "live"|"test", storage_profile_id, allowed_origins: string[], quota_bytes: number|null, used_bytes, disabled: boolean, created_at, updated_at }`

`slug`: `^[a-z][a-z0-9-]{1,31}$`.

## API keys

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/admin/api/projects/{id}/api-keys` | — | `200 { items: ApiKey[] }` |
| POST | `/admin/api/projects/{id}/api-keys` | `{ name, scopes: string[], expires_at? }` | `201 { api_key: ApiKey, secret }` — `secret` is shown **once** |
| DELETE | `/admin/api/api-keys/{key_id}` | — | `204` (revocation) |

`ApiKey`: `{ id, project_id, name, prefix, scopes, last_used_at, expires_at, revoked_at, created_at }`

Scopes: `uploads:write`, `assets:read`, `assets:write`, `assets:delete`, `urls:sign`, `namespaces:admin`, `imports:write`.

Secret format: `kd_{live|test}_{8 base62 chars — prefix}_{32 base62 chars}`; the stored `prefix` is `kd_live_XXXXXXXX`.

## Storage profiles

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/admin/api/storage-profiles` | — | `200 { items: StorageProfile[] }` |
| POST | `/admin/api/storage-profiles` | `StorageProfileInput` | `201 StorageProfile` |
| GET | `/admin/api/storage-profiles/{id}` | — | `200 StorageProfile` |
| PATCH | `/admin/api/storage-profiles/{id}` | partial `StorageProfileInput` (credentials only when changing them) | `200 StorageProfile` |
| DELETE | `/admin/api/storage-profiles/{id}` | — | `204` · `409 in_use` |
| POST | `/admin/api/storage-profiles/{id}/test` | — | `200 StorageTestResult` |

`StorageProfileInput`: `{ name, provider: "r2"|"s3"|"b2"|"hetzner"|"garage", endpoint, region, public_bucket, private_bucket, public_base_url, private_base_url?, access_key_id, secret_access_key, is_platform }`

`StorageProfile`: the same fields **without** `secret_access_key`; `access_key_id` is masked (`AKIA…3F2Q`); plus `{ id, last_test_at, last_test_ok, created_at, updated_at }`.

`StorageTestResult`: `{ ok, checks: [{ name, ok, detail, duration_ms }] }`. Checks: `private_put`, `private_head`, `private_delete`, `private_presign_put`, `public_put`, `public_get_via_base_url`, `public_delete`.

## Presets (M0 — read-only)

| Method | Path | Response |
|---|---|---|
| GET | `/admin/api/projects/{id}/presets` | `200 { items: Preset[] }` |

`Preset`: `{ id, slug, kind: "image"|"video", description, current_version, params: object, created_at }`

## System

| Method | Path | Response |
|---|---|---|
| GET | `/admin/api/system` | `200 System` |

`System`: `{ version, commit, started_at, uptime_sec, go_version, dev: boolean, db: { path, size_bytes, wal_bytes }, tmp: { path, free_bytes }, capabilities: { vips: Cap, vips_avif: Cap, ffmpeg: Cap, ffprobe: Cap, rclone: Cap, vaapi: Cap } }`

`Cap`: `{ ok, version?, detail? }`

## Audit (M0 — read-only)

`GET /admin/api/audit?limit=50&cursor=` → `200 { items: [{ id, actor_type, actor_id, project_id, action, target, details, ip, created_at }], next_cursor }`

## Webhooks (M1)

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/admin/api/projects/{id}/webhooks` | — | `200 { items: WebhookEndpoint[] }` |
| POST | `/admin/api/projects/{id}/webhooks` | `{ url, events? }` | `201 { webhook: WebhookEndpoint, secret }` — `secret` is shown **once** · `409 limit_reached` (20 endpoints per project) |
| DELETE | `/admin/api/webhooks/{id}` | — | `204` (its deliveries are deleted too) |
| POST | `/admin/api/webhooks/{id}/test` | — | `200 Delivery` — records a `webhook.test` event with one delivery to this endpoint and attempts it synchronously |

`WebhookEndpoint`: `{ id, project_id, url, events: string[], disabled: boolean, created_at }`

`url`: absolute `http(s)` URL without credentials (receivers usually live on the private network). `events`: exact types (`asset.ready`, `asset.failed`, `asset.updated`, `asset.deleted`, `upload.expired`, `quota.warning`, `quota.exceeded`, `namespace.purged`), prefix wildcards (`asset.*`, `upload.*`, `quota.*`, `namespace.*`) or `*`; default `["*"]`.

Secret format: `whsec_<base64 of 32 random bytes>`. It is stored encrypted (`webhook_endpoints.secret_enc`, AES-256-GCM envelope bound to the endpoint id). Requests follow [Standard Webhooks](https://www.standardwebhooks.com/): headers `webhook-id` (the event id `evt_…`), `webhook-timestamp` (unix seconds of the attempt) and `webhook-signature: v1,<base64(HMAC-SHA256(key, "{webhook-id}.{webhook-timestamp}.{body}"))>`, where `key` is the base64-decoded part after `whsec_`. Timeout 10 s; any non-2xx response (redirects are not followed) is a failure. Retries after 10 s, 30 s, 2 min, 10 min, 30 min, 1 h, 3 h, 6 h, 12 h; after the 10th failed attempt the delivery is `dead`.

## Webhook deliveries (M1)

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/admin/api/projects/{id}/deliveries?status=&limit=50&cursor=` | — | `200 { items: Delivery[], next_cursor }` (newest event first) |
| POST | `/admin/api/deliveries/{id}/retry` | — | `200 Delivery` — resets the attempt counter and schedules an immediate attempt · `409 in_progress` while an attempt is running |

`status`: `pending` · `delivering` · `delivered` · `dead`.

`Delivery`: `{ id, event_id, event_type, endpoint_id, endpoint_url, status, attempts, next_attempt_at (pending only), last_status_code, last_error, last_duration_ms, delivered_at, created_at }` (`created_at` is the event time).

Audit actions: `webhook.create`, `webhook.delete`, `webhook.test`, `delivery.retry`.
