# @kadr/node

The official Node.js SDK for **Kadr**, a self-hosted media platform.

- TypeScript, ESM and CommonJS builds, Node.js >= 20
- Zero runtime dependencies (global `fetch`, `FormData`, `Blob`, `node:crypto`)
- Local signing of private URLs (no network call)
- Standard Webhooks verification
- Automatic retries for safe requests, timeouts, typed RFC 9457 errors

## Install

The package is not published yet (`"private": true`, `"license": "UNLICENSED"`
until the distribution model is decided). Use it from this repository, e.g. a
workspace or `file:` dependency on `sdk/node`.

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
// ticket.kind === "upload": hand ticket.url / ticket.method / ticket.headers to the client,
// which PUTs the file directly to storage with exactly those headers.

// 2) Complete after the browser has uploaded to storage
const asset = await kadr.uploads.complete(ticket.uploadId, { wait: "5s" });

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
| `uploads.complete(uploadId, { wait?, idempotencyKey? }): Promise<Asset>` | `POST /v1/uploads/{id}/complete?wait=…` |
| `uploads.abort(uploadId, opts?): Promise<void>` | `DELETE /v1/uploads/{id}` |

`CreateUploadResult` is a discriminated union:

```ts
type CreateUploadResult =
  | ({ kind: "upload" } & UploadTicket)   // 201 — upload the file
  | { kind: "asset"; asset: Asset };      // 200 — deduplicated by sha256, asset is ready

type UploadTicket = { uploadId; method: "PUT"; url; headers; expiresAt };
```

`headers` always contains `Content-Type` and `Content-Length`; both are part of
the presigned signature, so send them unchanged (with `fetch`, let the body set
`Content-Length`). Files over 64 MB need multipart uploads, which the server
does not implement yet (`413 too_large`). `wait` may be a string sent verbatim
(`"5s"`) or a number of seconds (max 30 s); without it `complete()` returns the
asset in `processing` and an `asset.ready` / `asset.failed` webhook follows.

### Assets

| Method | HTTP |
|---|---|
| `assets.get(id, opts?): Promise<Asset>` | `GET /v1/assets/{id}` |
| `assets.listPage(params?, opts?): Promise<Page<Asset>>` | `GET /v1/assets?…` (one page) |
| `assets.list(params?, opts?): AsyncGenerator<Asset>` | follows `next_cursor` across all pages |
| `assets.update(id, { metadata?, visibility? }, opts?): Promise<Asset>` | `PATCH /v1/assets/{id}` (visibility changes: `501` for now) |
| `assets.delete(id, opts?): Promise<void>` | `DELETE /v1/assets/{id}` (`409 asset_processing` while processing) |
| `assets.upload(file, input, { idempotencyKey? }?): Promise<Asset>` | `POST /v1/assets?wait=…` (multipart/form-data) |

```ts
for await (const asset of kadr.assets.list({ namespace: "org_8f2c", status: "ready", limit: 100 })) {
  console.log(asset.id);
}

const page = await kadr.assets.listPage({ namespace: "org_8f2c" });
// page.items: Asset[], page.nextCursor: string | null
```

`assets.upload()` is for backend jobs on the private network. `file` may be a
`Blob`, `Buffer`/`Uint8Array`, `ArrayBuffer`, a web `ReadableStream` or any
`AsyncIterable<Uint8Array>` (e.g. a Node `fs.createReadStream()`). Streams are
buffered into memory first and rejected with `code: "too_large"` once they
exceed **25 MB** (`MAX_SERVER_UPLOAD_BYTES`), which is the server-side upload
limit. Larger files must use `uploads.create()` instead. The form contains the
fields `preset`, `namespace`, `visibility`, `filename`, `metadata`
(JSON-encoded) and `file` (with `contentType` as its part type). The server
hashes the file itself and deduplicates; `wait` works like in `complete()`.

```ts
import { readFile } from "node:fs/promises";

const asset = await kadr.assets.upload(await readFile("scan.jpg"), {
  preset: "doc-lite", namespace: "org_8f2c", visibility: "private",
  filename: "scan.jpg", contentType: "image/jpeg",
  metadata: { entity: "receipt", entityId: "rcp_123" }, wait: 10,
});
```

### Presets, namespaces, ping

| Method | HTTP |
|---|---|
| `presets.list(opts?): Promise<Preset[]>` | `GET /v1/presets` |
| `presets.get(slug, opts?): Promise<Preset>` | `GET /v1/presets/{slug}` |
| `namespaces.usage(ns, opts?): Promise<NamespaceUsage>` | `GET /v1/namespaces/{ns}/usage` |
| `namespaces.setQuota(ns, quota, opts?): Promise<NamespaceUsage>` | `PUT /v1/namespaces/{ns}/quota` |
| `ping(opts?): Promise<PingResult>` | `GET /v1/ping` |

`NamespaceUsage` is `{ namespace, usedBytes, reservedBytes, assetCount,
videoSecMonth, quotaBytes, quotaAssets, quotaVideoSecMonth }` (`null` limits =
unlimited). `setQuota(ns, { quotaBytes?, quotaAssets?, quotaVideoSecMonth? })`
replaces **all** limits — an omitted field becomes `null` — and returns the
usage after the change.

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
  `quotaBytes` → `quota_bytes`).
- Responses and webhook payloads are converted recursively
  (`upload_id` → `uploadId`, `preset_version` → `presetVersion`).
- **Exceptions:** the values of `metadata` keys (your data), `headers` keys
  (e.g. `Content-Type` in upload tickets) and preset `params` are passed
  through untouched in both directions. `metadata: { entityId: "rcp_1" }` is
  stored exactly like that.
- Query parameters are sent as written (`namespace`, `kind`, `status`, `limit`, `cursor`).
- Problem details keep the raw `errors` map (wire field name → message, e.g.
  `{ content_type: "required" }`); top-level problem keys are camelCased in `err.problem`.

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
| `code` | problem `code` (see `docs/API.md` → Errors, e.g. `quota_exceeded`, `validation_failed`, `upload_incomplete`, `asset_processing`); `http_<status>` when the body has no code or is not JSON; `invalid_response` when a 2xx body does not have the documented shape |
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
- Retry triggers: network errors, timeouts, HTTP `429` and `5xx` (except `501`),
  and — for keyed POSTs — `409 idempotency_in_progress` (the first request with
  that key is still running; the retry replays its stored response). Other
  `4xx` responses are never retried.
- Delay: exponential backoff with equal jitter (`retryBaseDelayMs * 2^attempt`,
  capped at `maxRetryDelayMs`). A `Retry-After` header (seconds or HTTP date)
  takes precedence, also capped at `maxRetryDelayMs`.
- `maxRetries` defaults to `2` (three attempts total). `timeoutMs` (default
  30 s) applies to each attempt, including reading the response body.

`uploads.complete()` is idempotent on the server, but it is still a POST: pass
an `idempotencyKey` (e.g. `` `complete:${uploadId}` ``) to get automatic retries.

## Signed URL canonical form

This is the exact contract the edge Worker must implement to verify URLs
produced by `signUrl()`. The Worker and signing-key management are not part of
this repository yet; the format below is the SDK's side of the contract.

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
`upload.expired`, `quota.warning`, `quota.exceeded`, and `webhook.test` from
the admin console's "Send test"), camelCased like API responses, with `id` set
to the `webhook-id` header. Payloads (all typed from `api/openapi.yaml`):

| `type` | `data` |
|---|---|
| `asset.ready`, `asset.deleted` | `{ asset, metadata }` (`asset.status` is `deleted` in `asset.deleted`) |
| `asset.failed` | `{ asset, metadata, error: { code, detail? } }` |
| `asset.updated` | `{ asset, metadata, changes: ["metadata"] }` |
| `upload.expired` | `{ uploadId, namespace, preset, visibility, filename, expiresAt, metadata }` |
| `quota.warning`, `quota.exceeded` | `{ namespace, usedBytes, quotaBytes, ratio }` (namespace byte quota) |
| `webhook.test` | `{ endpointId, message }` |

## Contract with the API

The public types are derived from `src/generated/openapi.ts`, which
`pnpm gen` generates from `api/openapi.yaml` (the API's source of truth after
the Go implementation). Request paths are checked against the generated
`paths`, and `test/contract.test.ts` pins the endpoint and event lists, so a
spec change that the SDK does not follow fails `pnpm typecheck`. CI runs
`pnpm gen:check` to make sure the committed file matches the spec.

Not wrapped because the server does not implement them yet (they answer
`501 not_implemented`): multipart uploads (`POST /v1/uploads/{id}/parts`),
`POST /v1/assets/{id}/reprocess`, `DELETE /v1/namespaces/{ns}` (and the
`namespace.purged` event), `POST /v1/signed-urls`, `/v1/imports/*` and
`PUT /v1/presets/{slug}`.

## Development

```sh
pnpm install
pnpm gen          # regenerate src/generated/openapi.ts after editing api/openapi.yaml
pnpm typecheck && pnpm lint && pnpm test && pnpm build
../../scripts/e2e-sdk.sh   # contract tests against a real server (pnpm test:e2e)
```
