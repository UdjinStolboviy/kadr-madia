# Integration Guide

How a product (any web or mobile app with a backend) integrates with Kadr. Target: **less than one day** per product.

## 1. Concepts

| Kadr concept | Typical mapping in your product |
|---|---|
| **Project** | One per product and environment: `acme` (live), `acme-staging` (test) |
| **API key** | `kd_live_…` stored in the backend as `KADR_API_KEY` — never shipped to clients |
| **Namespace** | Your tenant boundary: organization, workspace, household or user ID |
| **Preset** | A processing profile per use case: `photo-lite` for cards, `doc-lite` for receipts, `video-720p` for clips |
| **Visibility** | `public` (permanent CDN URL) or `private` (short-lived signed URL) |
| **Webhook** | `POST https://<your-backend>/internal/kadr/webhook`, reachable from Kadr |

Public URLs look like `https://cdn.example.com/acme/image/2026/11/blb_…/main-800x600.avif`; private ones like `https://media.example.com/acme/image/2026/11/blb_…/main-800x600.avif?exp=…&kid=…&sig=…`.

## 2. Setup checklist

1. In the Kadr admin console create the project, pick a storage profile, set quotas and allowed origins (used for the R2 CORS policy).
2. Create an API key with the scopes you need (`uploads:write`, `assets:read`, `assets:delete`, `urls:sign`).
3. Register a webhook endpoint and store its secret in your backend.
4. If you use private files, copy the active signing key (`kid` + secret) into your backend config.
5. Add your app origins to the CORS policy of the private bucket.

## 3. Backend (`@kadr/node`)

Works with any Node.js framework (NestJS, Express, Fastify, Next.js route handlers). SDKs for other languages are generated from `api/openapi.yaml`.

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
if (ticket.kind === "asset") return ticket.asset; // deduplicated by sha256: already stored
// otherwise hand ticket.url / ticket.method / ticket.headers to the client

// 2) Complete after the browser has uploaded to storage
const asset = await kadr.uploads.complete(ticket.uploadId, { wait: "5s" });

// 3) Private link — signed locally, no request to Kadr
const url = kadr.signUrl(asset.renditions[0].key!, { ttl: 3600 });

// 4) Webhook receiver
const event = kadr.webhooks.verify(rawBody, headers, process.env.KADR_WEBHOOK_SECRET!);
```

### What to store in your database

| Field | Why |
|---|---|
| `kadr_asset_id` | Reference for delete / reprocess / status |
| `status` | `processing` → `ready` / `failed` (updated by webhook) |
| `url` or `key` | Public URL to render directly, or private key to sign on read |
| `thumbhash`, `width`, `height` | Instant placeholder and layout without CLS |

### Deleting data

When an entity in your product is deleted, call `kadr.assets.delete(id)` (ideally from an outbox/background job). For tenant offboarding or GDPR erasure, `DELETE /v1/namespaces/{ns}` is planned (not implemented yet; delete the namespace's assets individually until then).

### Server-side ingestion

Backend jobs (scanners, imports, generated images) can send files ≤ 25 MB straight to `POST /v1/assets` over the private network, or (once implemented) ask Kadr to fetch a URL with `POST /v1/imports/url`.

## 4. Frontend (`@kadr/web`, `@kadr/react`)

```tsx
import { uploadFile } from "@kadr/web";
import { KadrImage } from "@kadr/react";

const asset = await uploadFile(file, {
  createTicket: (meta) => api.post("/media/uploads", meta),          // via your backend
  complete: (id, parts) => api.post(`/media/uploads/${id}/complete`, { parts }),
  preResize: { maxSide: 1600, quality: 0.9 },                         // Web Worker + OffscreenCanvas
  onProgress: (p) => setProgress(p),
});

<KadrImage asset={asset} sizes="(max-width: 640px) 100vw, 400px" />
// → <picture> with AVIF (plus WebP when the preset stores it), ThumbHash placeholder while loading, width/height with no CLS
```

Without the SDK: `PUT` the file to `ticket.url` with exactly `ticket.headers`; for `MULTIPART` tickets upload each part and collect the `ETag` response headers.

## 5. Migrating an existing media library

1. **Shadow:** create a `-staging` project and route staging uploads through Kadr.
2. **Feature flag:** enable Kadr for a subset of tenants in production.
3. **Backfill:** iterate over existing media → `POST /v1/imports/url` → store the new references; delete old files after verification.
4. **Switch the default**, monitor failed rate and processing p95 in the admin console.
