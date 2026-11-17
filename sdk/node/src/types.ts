/**
 * Hand-written types for the Kadr API v1 (see docs/API.md).
 *
 * All types here describe the SDK-facing shape, which is camelCase. The wire
 * format is snake_case; the SDK converts keys in both directions (see
 * `case.ts`). User-defined `metadata` objects and upload `headers` are passed
 * through untouched.
 *
 * String unions are open (`(string & {})`) where the server may add new values
 * without a breaking change.
 */

/** A string union that still accepts unknown future values. */
export type OpenString<T extends string> = T | (string & {});

export type Visibility = "public" | "private";

export type AssetStatus = OpenString<"processing" | "ready" | "failed">;

export type AssetKind = OpenString<"image" | "video" | "audio" | "document" | "other">;

/** Free-form metadata attached by your product. Never key-converted. */
export type Metadata = Record<string, unknown>;

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

export interface AssetOriginal {
  filename: string;
  contentType: string;
  bytes: number;
  width?: number | null;
  height?: number | null;
}

export interface Rendition {
  name: string;
  format: string;
  bytes: number;
  width?: number | null;
  height?: number | null;
  /** Permanent CDN URL. Present for `public` assets only. */
  url?: string;
  /** Storage key to sign with `kadr.signUrl()`. Present for `private` assets only. */
  key?: string;
}

export interface AssetPlaceholder {
  thumbhash?: string | null;
  dominantColor?: string | null;
}

export interface AssetError {
  code: OpenString<"unsupported_media" | "too_long" | "corrupt" | "timeout">;
  message?: string;
}

export interface Asset {
  id: string;
  status: AssetStatus;
  kind: AssetKind;
  namespace: string;
  visibility: Visibility;
  preset: string;
  presetVersion: number;
  original: AssetOriginal;
  renditions: Rendition[];
  placeholder?: AssetPlaceholder | null;
  durationSec?: number | null;
  poster?: Rendition | null;
  metadata: Metadata;
  savingsRatio?: number | null;
  createdAt: string;
  readyAt?: string | null;
  /** Present when `status === "failed"`. */
  error?: AssetError | null;
}

export interface ListAssetsParams {
  namespace?: string;
  kind?: AssetKind;
  status?: AssetStatus;
  /** Page size (server default applies when omitted). */
  limit?: number;
  /** Cursor from a previous page's `nextCursor`. */
  cursor?: string;
}

/**
 * One page of a cursor-paginated list.
 *
 * ASSUMPTION: the wire envelope is `{ "data": [...], "next_cursor": string | null }`.
 * An `items` array is accepted as a fallback.
 */
export interface Page<T> {
  data: T[];
  nextCursor: string | null;
}

export interface UpdateAssetInput {
  metadata?: Metadata;
  /** Changing visibility triggers a cross-bucket copy on the server. */
  visibility?: Visibility;
}

export interface ReprocessAssetInput {
  /** Preset slug to reprocess with. Defaults to the latest version of the current preset. */
  preset?: string;
}

export interface UploadAssetInput {
  preset: string;
  namespace: string;
  visibility: Visibility;
  filename?: string;
  contentType?: string;
  sha256?: string;
  metadata?: Metadata;
}

// ---------------------------------------------------------------------------
// Uploads
// ---------------------------------------------------------------------------

export interface CreateUploadInput {
  preset: string;
  namespace: string;
  visibility: Visibility;
  filename?: string;
  contentType: string;
  size: number;
  /** Hex-encoded SHA-256 of the file. Enables deduplication. */
  sha256?: string;
  metadata?: Metadata;
}

export interface UploadPartUrl {
  number: number;
  url: string;
}

interface UploadTicketBase {
  uploadId: string;
  /** ISO 8601 timestamp after which the ticket is no longer valid. */
  expiresAt: string;
}

/** Single-request upload: `PUT` the file to `url` with exactly `headers`. */
export interface SingleUploadTicket extends UploadTicketBase {
  method: "PUT";
  url: string;
  /** HTTP headers to send verbatim with the PUT. Never key-converted. */
  headers: Record<string, string>;
}

/**
 * Multipart upload (files > 64 MB): upload each part to its URL, collect the
 * `ETag` response headers and pass them to `uploads.complete()`.
 * `parts` contains the first 20 parts; request more via `uploads.parts()`.
 */
export interface MultipartUploadTicket extends UploadTicketBase {
  method: "MULTIPART";
  partSize: number;
  parts: UploadPartUrl[];
  headers?: Record<string, string>;
}

export type UploadTicket = SingleUploadTicket | MultipartUploadTicket;

/**
 * Result of `uploads.create()`.
 *
 * - `kind: "upload"` (HTTP 201): a ticket; the client must upload the file.
 *   The ticket fields are flattened onto the result (`result.uploadId`, ...).
 * - `kind: "asset"` (HTTP 200): the file was deduplicated by `sha256`; the
 *   ready asset is returned immediately and no upload is needed.
 */
export type CreateUploadResult =
  | ({ kind: "upload" } & UploadTicket)
  | { kind: "asset"; asset: Asset };

export interface CompletedPart {
  number: number;
  etag: string;
}

export interface CompleteUploadInput {
  /** Required for multipart uploads. */
  parts?: CompletedPart[];
  /**
   * How long the server may wait for processing to finish before returning.
   * A number is interpreted as seconds; a string is sent verbatim (e.g. `"5s"`).
   */
  wait?: number | string;
}

// ---------------------------------------------------------------------------
// Presets
// ---------------------------------------------------------------------------

export interface Preset {
  slug: string;
  version: number;
  name?: string;
  kind?: AssetKind;
  /** Processing parameters. Shape depends on the preset kind. */
  params?: Record<string, unknown>;
  createdAt?: string;
  updatedAt?: string;
  [key: string]: unknown;
}

// ---------------------------------------------------------------------------
// Namespaces
// ---------------------------------------------------------------------------

export interface NamespaceQuota {
  /** Maximum stored bytes. `null` means unlimited. */
  maxBytes?: number | null;
  /** Maximum number of assets. `null` means unlimited. */
  maxAssets?: number | null;
  /** Maximum processed video minutes per calendar month. `null` means unlimited. */
  maxVideoMinutesPerMonth?: number | null;
  [key: string]: unknown;
}

export interface NamespaceUsage {
  namespace: string;
  bytes: number;
  count: number;
  videoMinutesThisMonth?: number;
  limits?: NamespaceQuota;
  [key: string]: unknown;
}

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

export type WebhookEventType =
  | "asset.ready"
  | "asset.failed"
  | "asset.updated"
  | "asset.deleted"
  | "upload.expired"
  | "quota.warning"
  | "quota.exceeded"
  | "namespace.purged";

interface WebhookEventBase<T extends WebhookEventType, D> {
  /** Value of the `webhook-id` header. Use it to deduplicate deliveries. */
  id: string;
  type: T;
  /** ISO 8601 timestamp from the payload. */
  timestamp: string;
  data: D;
}

export interface AssetEventData {
  asset: Asset;
  metadata?: Metadata;
  [key: string]: unknown;
}

export type AssetReadyEvent = WebhookEventBase<"asset.ready", AssetEventData>;
export type AssetFailedEvent = WebhookEventBase<
  "asset.failed",
  AssetEventData & { error: AssetError }
>;
export type AssetUpdatedEvent = WebhookEventBase<"asset.updated", AssetEventData>;
export type AssetDeletedEvent = WebhookEventBase<"asset.deleted", AssetEventData>;
export type UploadExpiredEvent = WebhookEventBase<
  "upload.expired",
  { uploadId: string; namespace?: string; metadata?: Metadata; [key: string]: unknown }
>;
export interface QuotaEventData {
  namespace: string;
  usage?: NamespaceUsage;
  [key: string]: unknown;
}
export type QuotaWarningEvent = WebhookEventBase<"quota.warning", QuotaEventData>;
export type QuotaExceededEvent = WebhookEventBase<"quota.exceeded", QuotaEventData>;
export type NamespacePurgedEvent = WebhookEventBase<
  "namespace.purged",
  { namespace: string; [key: string]: unknown }
>;

export type WebhookEvent =
  | AssetReadyEvent
  | AssetFailedEvent
  | AssetUpdatedEvent
  | AssetDeletedEvent
  | UploadExpiredEvent
  | QuotaWarningEvent
  | QuotaExceededEvent
  | NamespacePurgedEvent;

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** RFC 9457 problem details as returned by the API (camelCased). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
  code?: string;
  errors?: unknown[];
  [key: string]: unknown;
}
