/**
 * Wire types. These mirror the JSON the Kadr API returns in an upload ticket
 * (`POST /v1/uploads`, snake_case) and the body of the `complete` call, so a
 * customer backend can forward them unchanged.
 */

/** Single-request upload: PUT the body to `url` with exactly `headers`. */
export interface PutUploadTicket {
  upload_id: string;
  method: "PUT";
  url: string;
  headers?: Record<string, string>;
  expires_at?: string;
}

export interface MultipartPart {
  /** 1-based part number. */
  part_number: number;
  /** Pre-signed URL for this part (PUT). */
  url: string;
  /** Extra headers to send with this part, if the server signed any. */
  headers?: Record<string, string>;
}

/** Multipart upload: the file is cut into `part_size` chunks, one per entry in `parts`. */
export interface MultipartUploadTicket {
  upload_id: string;
  method: "MULTIPART";
  /** Bytes per part; the last part may be shorter. */
  part_size: number;
  parts: MultipartPart[];
  expires_at?: string;
  session_expires_at?: string;
}

export type UploadTicket = PutUploadTicket | MultipartUploadTicket;

/** One uploaded part as sent to `complete`. */
export interface CompletedPart {
  part_number: number;
  etag: string;
}

/** Body of `POST /v1/uploads/{id}/complete` for multipart uploads: `{ "parts": [...] }`. */
export interface CompleteBody {
  parts: CompletedPart[];
}

/** What `createTicket` receives: describes the (possibly resized) bytes that will be uploaded. */
export interface TicketRequest {
  filename: string;
  content_type: string;
  /** Exact byte size of the payload (after pre-resize). */
  size: number;
}

export interface UploadProgress {
  loaded: number;
  total: number;
  /** `loaded / total`, 0..1. */
  fraction: number;
  /** Set for multipart uploads when the event belongs to a single part. */
  partNumber?: number;
}

export interface RetryOptions {
  /** Retries after the first attempt (default 3). */
  retries?: number;
  /** First backoff delay in ms (default 500); doubles each attempt with jitter. */
  baseDelayMs?: number;
  /** Upper bound for a single delay in ms (default 8000). */
  maxDelayMs?: number;
}

export interface PreResizeOptions {
  /** Longest side in pixels; larger images are scaled down (default 2048). */
  maxSide?: number;
  /** Encoder quality 0..1 for lossy output (default 0.9). */
  quality?: number;
  /** Force an output type. Default: keep png/webp, otherwise jpeg. */
  outputType?: "image/jpeg" | "image/webp" | "image/png";
}

export interface UploadOptions extends RetryOptions {
  signal?: AbortSignal | undefined;
  onProgress?: ((p: UploadProgress) => void) | undefined;
  /** Parallel part uploads for MULTIPART tickets (default 4). */
  concurrency?: number;
  /** Per-attempt timeout in ms; 0 or unset disables it. */
  timeoutMs?: number;
  /** Already-uploaded parts for resuming a MULTIPART upload (e.g. from `GET /v1/uploads/{id}`). */
  completedParts?: CompletedPart[] | undefined;
  /** Called as each individual part of a MULTIPART upload completes (useful for persistence). */
  onPartComplete?: ((part: CompletedPart) => void) | undefined;
  /** Called when a part PUT fails with HTTP 403 (expired presigned URL) to obtain fresh part URLs. */
  refreshParts?: ((uploadId: string, partNumbers: number[]) => Promise<MultipartPart[]>) | undefined;
  /** Replace the XHR constructor (tests, polyfills). */
  createXhr?: (() => XMLHttpRequest) | undefined;
  /** Replace the backoff sleep (tests). */
  sleep?: ((ms: number, signal?: AbortSignal) => Promise<void>) | undefined;
}

export interface UploadFileOptions<TAsset> extends UploadOptions {
  /** Ask your backend for a ticket (it calls `kadr.uploads.create`). */
  createTicket: (req: TicketRequest) => Promise<UploadTicket>;
  /** Ask your backend to complete the upload (it calls `kadr.uploads.complete`). */
  complete: (uploadId: string, parts?: CompletedPart[]) => Promise<TAsset>;
  /** Resize images in a Web Worker first. `false`/omitted uploads the original. */
  preResize?: PreResizeOptions | false;
}
