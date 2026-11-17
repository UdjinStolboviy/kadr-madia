# @kadr/node

The official Node.js SDK for **Kadr**, a self-hosted media platform.

- TypeScript, ESM and CommonJS builds, Node.js >= 20
- Zero runtime dependencies (global `fetch`, `FormData`, `Blob`, `node:crypto`)
- Local signing of private URLs (no network call)
- Standard Webhooks verification
- Automatic retries for safe requests, timeouts, typed RFC 9457 errors

## Install

```sh
pnpm add @kadr/node
# or: npm install @kadr/node
```

## Quick start

```ts
import { Kadr } from "@kadr/node";

export const kadr = new Kadr({
  baseUrl: process.env.KADR_URL!,          // e.g. http://kadr.internal:8080
  apiKey: process.env.KADR_API_KEY!,
  signing: { kid: process.env.KADR_SIGNING_KID!, secret: process.env.KADR_SIGNING_SECRET!, baseUrl: "https://media.example.com" },
});

// 1) Issue an upload ticket after checking the user's access to the tenant
const ticket = await kadr.uploads.create({
  preset: "photo-lite", namespace: orgId, visibility: "private",
  contentType, size, sha256, metadata: { entity: "receipt", entityId },
}, { idempotencyKey: `${userId}:${clientUploadId}` });

if (ticket.kind === "asset") {
  // Deduplicated by sha256: the file already exists, no upload needed.
  return ticket.asset;
}
// ticket.kind === "upload": hand ticket.url / ticket.headers (or ticket.parts) to the client.

// 2) Complete after the browser has uploaded to storage
const asset = await kadr.uploads.complete(ticket.uploadId, { parts, wait: "5s" });

// 3) Private link — signed locally, no request to Kadr
const key = asset.renditions[0]?.key;
const url = key ? kadr.signUrl(key, { ttl: 3600 }) : undefined;

// 4) Webhook receiver
const event = kadr.webhooks.verify(rawBody, headers, process.env.KADR_WEBHOOK_SECRET!);
```

The only difference from the snippet in `docs/INTEGRATION.md` is that
`uploads.create()` returns a discriminated result, so you narrow on
`ticket.kind` before reading `ticket.uploadId`.

## Client options

```ts
new Kadr({
  baseUrl: "http://kadr.internal:8080", // "/v1" is appended; a trailing slash or an explicit "/v1" is fine
  apiKey: "kd_live_…",                  // sent as "Authorization: Bearer …"
  signing: { kid, secret, baseUrl },     // required only for signUrl()
  timeoutMs: 30_000,                     // per attempt
  maxRetries: 2,
  retryBaseDelayMs: 500,
  maxRetryDelayMs: 30_000,               // caps both backoff and Retry-After
  fetch: customFetch,                    // defaults to globalThis.fetch
  userAgent: "acme-backend/1.0",
});
```

Every method accepts a final options argument with `signal?: AbortSignal` and
`timeoutMs?: number`. POST methods also accept `idempotencyKey?: string`, sent
as the `Idempotency-Key` header.

## API surface

### Uploads

| Method | HTTP |
|---|---|
| `uploads.create(input, { idempotencyKey? }): Promise<CreateUploadResult>` | `POST /v1/uploads` |
| `uploads.parts(uploadId, partNumbers, opts?): Promise<UploadPartUrl[]>` | `POST /v1/uploads/{id}/parts` |
| `uploads.complete(uploadId, { parts?, wait?, idempotencyKey? }): Promise<Asset>` | `POST /v1/uploads/{id}/complete?wait=…` |
| `uploads.abort(uploadId, opts?): Promise<void>` | `DELETE /v1/uploads/{id}` |

`CreateUploadResult` is a discriminated union:

```ts
type CreateUploadResult =
  | ({ kind: "upload" } & UploadTicket)   // 201 — upload the file
  | { kind: "asset"; asset: Asset };      // 200 — deduplicated by sha256, asset is ready

type UploadTicket =
  | { method: "PUT"; uploadId; url; headers; expiresAt }
  | { method: "MULTIPART"; uploadId; partSize; parts: { number; url }[]; expiresAt };
```

For `MULTIPART` tickets the first 20 part URLs are included; get more with
`uploads.parts(id, [21, 22, …])`. Upload each part, collect the `ETag`
response headers and pass `{ number, etag }[]` to `uploads.complete()`.
`wait` may be a string sent verbatim (`"5s"`) or a number of seconds.

### Assets

| Method | HTTP |
|---|---|
| `assets.get(id, opts?): Promise<Asset>` | `GET /v1/assets/{id}` |
| `assets.listPage(params?, opts?): Promise<Page<Asset>>` | `GET /v1/assets?…` (one page) |
| `assets.list(params?, opts?): AsyncGenerator<Asset>` | follows `next_cursor` across all pages |
| `assets.update(id, { metadata?, visibility? }, opts?): Promise<Asset>` | `PATCH /v1/assets/{id}` |
| `assets.delete(id, opts?): Promise<void>` | `DELETE /v1/assets/{id}` |
| `assets.reprocess(id, { preset? }?, { idempotencyKey? }?): Promise<Asset>` | `POST /v1/assets/{id}/reprocess` |
| `assets.upload(file, input, { idempotencyKey? }?): Promise<Asset>` | `POST /v1/assets` (multipart/form-data) |

```ts
for await (const asset of kadr.assets.list({ namespace: "org_8f2c", status: "ready", limit: 100 })) {
  console.log(asset.id);
}

const page = await kadr.assets.listPage({ namespace: "org_8f2c" });
// page.data: Asset[], page.nextCursor: string | null
```

`assets.upload()` is for backend jobs on the private network. `file` may be a
`Blob`, `Buffer`/`Uint8Array`, `ArrayBuffer`, a web `ReadableStream` or any
`AsyncIterable<Uint8Array>` (e.g. a Node `fs.createReadStream()`). Streams are
buffered into memory first and rejected with `code: "too_large"` once they
exceed **25 MB** (`MAX_SERVER_UPLOAD_BYTES`), which is the server-side upload
limit. Larger files must use `uploads.create()` instead. The form contains the
fields `preset`, `namespace`, `visibility`, `filename`, `content_type`,
`sha256`, `metadata` (JSON-encoded) and `file`.

```ts
import { readFile } from "node:fs/promises";

const asset = await kadr.assets.upload(await readFile("scan.pdf"), {
  preset: "doc-lite", namespace: "org_8f2c", visibility: "private",
  filename: "scan.pdf", contentType: "application/pdf",
  metadata: { entity: "receipt", entityId: "rcp_123" },
});
```

### Presets and namespaces

| Method | HTTP |
|---|---|
| `presets.list(opts?): Promise<Preset[]>` | `GET /v1/presets` |
| `presets.get(slug, opts?): Promise<Preset>` | `GET /v1/presets/{slug}` |
| `namespaces.usage(ns, opts?): Promise<NamespaceUsage>` | `GET /v1/namespaces/{ns}/usage` |
| `namespaces.setQuota(ns, quota, opts?): Promise<NamespaceQuota>` | `PUT /v1/namespaces/{ns}/quota` |
| `namespaces.purge(ns, opts?): Promise<void>` | `DELETE /v1/namespaces/{ns}` |

### Standalone helpers

```ts
import { signUrl, verifySignedUrl, verifyWebhook, signWebhook } from "@kadr/node";
```

- `signUrl(signingConfig, key, { ttl, download?, now? }): string`
- `verifySignedUrl(url, { secrets, now?, pathPrefix? })`
- `verifyWebhook(rawBody, headers, secret, { now?, toleranceSeconds? }): WebhookEvent`
- `signWebhook(secret, { id, timestamp, body }): string` — returns `v1,<base64>`

## Case conversion

The wire format is snake_case; the SDK is camelCase in both directions:

- Request bodies are converted recursively (`contentType` → `content_type`,
  `partNumbers` → `part_numbers`, `maxBytes` → `max_bytes`).
- Responses and webhook payloads are converted recursively
  (`upload_id` → `uploadId`, `preset_version` → `presetVersion`).
- **Exceptions:** the values of `metadata` keys (your data) and `headers` keys
  (e.g. `Content-Type` in upload tickets) are passed through untouched in both
  directions. `metadata: { entityId: "rcp_1" }` is stored exactly like that.
- Query parameters are sent as written (`namespace`, `kind`, `status`, `limit`, `cursor`).
- Problem details keep the raw `errors` array; top-level problem keys are camelCased in `err.problem`.

## Errors

Every failed call throws a `KadrError`:

```ts
import { KadrError } from "@kadr/node";

try {
  await kadr.uploads.create(input);
} catch (err) {
  if (err instanceof KadrError && err.code === "quota_exceeded") { … }
}
```

| Field | Source |
|---|---|
| `status` | HTTP status, `0` when no response was received |
| `code` | problem `code` (e.g. `quota_exceeded`, `unsupported_media`, `too_large`, `preset_not_found`); `http_<status>` when the body has no code or is not JSON |
| `title`, `detail`, `type`, `instance`, `errors` | RFC 9457 problem fields |
| `requestId` | `x-request-id` (or `request-id`) response header |
| `problem` | the full problem document |

Transport errors use subclasses: `KadrNetworkError` (`code: "network_error"`),
`KadrTimeoutError` (`code: "timeout"`) and `KadrAbortError` (`code: "aborted"`,
caller's `AbortSignal`). Non-JSON error bodies still produce a `KadrError`
with the body text as `detail`.

## Retries and timeouts

- Retried: **GET** requests, and **POST** requests that carry an `idempotencyKey`.
- Never retried: POST without an idempotency key, PUT, PATCH, DELETE. They are
  not guaranteed idempotent at the HTTP layer (PATCH can trigger a
  cross-bucket copy; a retried DELETE may return `404` after a lost success
  response), so the caller decides.
- Retry triggers: network errors, timeouts, HTTP `429` and `5xx` (except `501`).
  Other `4xx` responses are never retried.
- Delay: exponential backoff with equal jitter (`retryBaseDelayMs * 2^attempt`,
  capped at `maxRetryDelayMs`). A `Retry-After` header (seconds or HTTP date)
  takes precedence, also capped at `maxRetryDelayMs`.
- `maxRetries` defaults to `2` (three attempts total). `timeoutMs` (default
  30 s) applies to each attempt, including reading the response body.

`uploads.complete()` is idempotent on the server, but it is still a POST: pass
an `idempotencyKey` (e.g. `` `complete:${uploadId}` ``) to get automatic retries.

## Signed URL canonical form

This is the exact contract the edge Worker must implement to verify URLs
produced by `signUrl()`.

**URL format**

```
{signing.baseUrl}/{encodedKey}?exp={exp}&kid={kid}&sig={sig}[&download={download}]
```

- `encodedKey`: the normalized key with each `/`-separated segment
  percent-encoded via `encodeURIComponent`; `/` separators are kept.
- `kid` and `download` are percent-encoded with `encodeURIComponent`. `sig` is
  base64url and needs no encoding. `download` is omitted when not given (an
  empty string is treated as not given).

**Key normalization**

1. Strip all leading `/` characters from the key.
2. The key must be non-empty and must not contain `.` or `..` segments
   (they would be collapsed by URL parsers).
3. The canonical path is `"/" + key`, using the **raw, un-encoded** key.

**Canonical string** (UTF-8, fields separated by a single `\n` (LF), no trailing newline):

```
GET\n/{key}\n{exp}\n{download}
```

- `GET`: literal, uppercase.
- `/{key}`: canonical path from above (raw, not percent-encoded).
- `{exp}`: Unix time in seconds as a base-10 integer without leading zeros or
  sign; `exp = floor(now_ms / 1000) + ttl`.
- `{download}`: the raw (decoded) download filename, or the empty string when
  absent. The line separator before it is always present.

Example: `GET\n/acme/blb_01JD/main.avif\n1763330400\nreceipt.avif`

**Signature**

```
sig = base64url_nopad( HMAC-SHA256( key = UTF-8 bytes of the secret string, message = canonical string ) )
```

The secret is used **as the raw UTF-8 bytes of the configured string**; it is
not hex- or base64-decoded. Kadr issues 32-byte secrets in a printable
encoding, and that printable text itself is the HMAC key.

**Verification (Worker)**

1. Parse `exp`, `kid`, `sig` (all required) and optional `download` from the
   query string (URL-decoded). `exp` must match `^\d+$`.
2. Take the URL pathname, remove any mount prefix of the delivery host, split
   on `/`, `decodeURIComponent` each segment, re-join with `/`, strip leading
   slashes. That is the key.
3. Look up the secret by `kid` (`active` and `verify_only` keys); unknown `kid` → reject.
4. Recompute `sig` and compare in **constant time** (length check first).
5. Reject if `floor(now_ms / 1000) > exp` (the URL is valid up to and including second `exp`).
6. Exclude `exp`, `kid`, `sig` from the cache key.

`verifySignedUrl(url, { secrets, now?, pathPrefix? })` is the reference
implementation. It returns
`{ ok: true, key, exp, kid, download? }` or
`{ ok: false, reason: "malformed" | "missing_params" | "unknown_kid" | "bad_signature" | "expired" }`.
The signature is checked before expiry, so `expired` is only reported for authentic URLs.

## Webhook verification

Kadr sends webhooks in the [Standard Webhooks](https://www.standardwebhooks.com/)
format. Always verify against the **raw** request body:

```ts
// Express
app.post("/internal/kadr/webhook", express.raw({ type: "application/json" }), (req, res) => {
  let event;
  try {
    event = kadr.webhooks.verify(req.body, req.headers, process.env.KADR_WEBHOOK_SECRET!);
  } catch (err) {
    return res.status(400).end();
  }
  switch (event.type) {
    case "asset.ready":  /* event.data.asset */ break;
    case "asset.failed": /* event.data.error.code */ break;
  }
  res.status(204).end(); // deduplicate by event.id (the webhook-id header)
});
```

- `rawBody`: `string`, `Buffer` or `Uint8Array`, exactly as received.
- `headers`: a `Headers` object or a plain record (case-insensitive; array values use the first item).
- Signed content: `${webhook-id}.${webhook-timestamp}.${body}`, HMAC-SHA256, base64.
- `webhook-signature` may contain several space-separated entries
  (`v1,<base64> v1,<base64>`); any matching `v1` entry is accepted. The
  comparison is constant-time.
- `webhook-timestamp` must be within **5 minutes** of the current time in either direction.
- **Secret format:** `whsec_<base64>` is stripped of its prefix and
  base64-decoded into the HMAC key. A secret without the `whsec_` prefix is
  used as raw UTF-8 bytes.
- On failure a `WebhookVerificationError` is thrown.

The result is a `WebhookEvent` discriminated union on `type`
(`asset.ready`, `asset.failed`, `asset.updated`, `asset.deleted`,
`upload.expired`, `quota.warning`, `quota.exceeded`, `namespace.purged`),
camelCased like API responses, with `id` set to the `webhook-id` header.

## Assumptions

These points are not yet fixed by `docs/API.md` (there is no `openapi.yaml`
yet); the SDK is tolerant where it can be:

- **List envelope:** `{ "data": [...], "next_cursor": string | null }`. An
  `items` array or a bare array is also accepted; an empty or missing
  `next_cursor` ends iteration. The same envelope is assumed for `GET /v1/presets`.
- **Dedup response (200 from `POST /v1/uploads`):** either the asset object
  itself or `{ "asset": {...} }`. A body containing `upload_id` is always
  treated as a ticket.
- **Asset endpoints** return the bare asset (an `{ "asset": … }` wrapper is also accepted).
- `uploads.parts()` returns `{ "parts": [...] }`; the SDK returns the array.
- `namespaces.setQuota()` returns the stored quota; on an empty body the input is returned.
  Quota and usage field names (`max_bytes`, `max_assets`,
  `max_video_minutes_per_month`, `video_minutes_this_month`) are provisional.
- Webhook `data` shapes other than `asset.*` (`upload.expired`, `quota.*`,
  `namespace.purged`) are provisional and typed loosely.
- `DELETE` endpoints may answer `204` or `202`; bodies are ignored.

## Development

```sh
pnpm install
pnpm typecheck && pnpm lint && pnpm test && pnpm build
```
