# @kadr/web

Browser uploader for **Kadr**. The browser never holds an API key: your backend
creates the upload ticket and completes the upload (via `@kadr/node`), this
package moves the bytes.

- Optional image pre-resize in a Web Worker with `OffscreenCanvas` (falls back to the original file when unsupported or on any error)
- `PUT` via `XMLHttpRequest` for real upload progress
- Retries with exponential backoff and jitter (network errors, 408, 429, 5xx)
- `AbortSignal` support
- Multipart uploads: parallel parts (default 4), per-part retries, ETag collection
- Zero runtime dependencies, ESM + CJS, ships types

Not published yet (`"private": true`); use it from this repository.

## Usage

```ts
import { uploadFile } from "@kadr/web";

const asset = await uploadFile(file, {
  createTicket: (req) => api.post("/media/uploads", req),              // your backend -> kadr.uploads.create
  complete: (uploadId, parts) => api.post(`/media/uploads/${uploadId}/complete`, { parts }), // -> kadr.uploads.complete
  preResize: { maxSide: 1600, quality: 0.9 },
  onProgress: (p) => setProgress(p.fraction),
  signal: abortController.signal,
});
```

`createTicket` receives `{ filename, content_type, size }` of the bytes that will
actually be uploaded (after pre-resize), so the exact `size` required by
`POST /v1/uploads` is correct. It must return the ticket JSON as the API
returned it. `complete` receives `parts` only for multipart tickets (otherwise
`undefined`); forward them as the request body.

## Ticket contract

```ts
// method: "PUT"
{ upload_id, method: "PUT", url, headers?, expires_at? }

// method: "MULTIPART"
{ upload_id, method: "MULTIPART", part_size,
  parts: [{ number: 1, url, headers? }, ...] }
```

The file is cut into `part_size` chunks (the last may be shorter); part `n`
covers bytes `[(n-1)*part_size, n*part_size)`. The number of `parts` must equal
`ceil(size / part_size)`. Each part is `PUT` to its URL and the `ETag` response
header is collected. The body of `complete` is:

```json
{ "parts": [{ "part_number": 1, "etag": "\"9b2cf535f27731c974343645a3985328\"" }] }
```

ETags are passed through exactly as received (quotes included), sorted by part
number. The storage bucket's CORS configuration must expose the `ETag` header
(`ExposeHeaders: ["ETag"]`); otherwise the upload fails with a clear error.
`Content-Length` from ticket headers is never set manually (the browser
forbids it); all other ticket headers are sent as-is.

All types (`UploadTicket`, `PutUploadTicket`, `MultipartUploadTicket`,
`CompletedPart`, `CompleteBody`, ...) are exported.

## API

| Export | |
|---|---|
| `uploadFile(file, options)` | Whole flow: resize, ticket, upload, complete. Resolves with whatever `complete` returns |
| `uploadToTicket(blob, ticket, options)` | Upload only; returns the `CompletedPart[]` for multipart, `undefined` for PUT |
| `putToTicket`, `putMultipart` | The two upload strategies separately |
| `preResizeImage(blob, filename, { maxSide, quality, outputType })` | Resize helper; returns the original on any failure |
| `resizeSupported()` | Feature detection for Worker + OffscreenCanvas + createImageBitmap |
| `KadrUploadError`, `KadrAbortError` | `KadrUploadError.status` / `.retryable` / `.partNumber`; aborts throw an `AbortError` |

Options: `concurrency` (4), `retries` (3), `baseDelayMs` (500), `maxDelayMs`
(8000), `timeoutMs` (per attempt, off by default), `onProgress({ loaded, total,
fraction, partNumber? })`, `signal`.

Pre-resize only touches jpeg/png/webp/avif/bmp, only downscales (images already
within `maxSide` are uploaded untouched), keeps png/webp, otherwise encodes
jpeg, and strips EXIF metadata (orientation is applied). The Worker is created
from a blob URL, so a strict CSP needs `worker-src blob:`.

## Development

```sh
pnpm install
pnpm typecheck && pnpm lint && pnpm test && pnpm build
```
