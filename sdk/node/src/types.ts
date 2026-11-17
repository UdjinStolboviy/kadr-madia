/**
 * Public types of the Kadr API v1, derived from the generated OpenAPI types
 * (`src/generated/openapi.ts`, produced from `api/openapi.yaml` by `pnpm gen`).
 *
 * The wire format is snake_case; the SDK surface is camelCase and the SDK
 * converts keys in both directions (see `case.ts`). The values of `metadata`
 * (your data), upload `headers` and preset `params` are passed through
 * verbatim.
 */
import type { Camelize, JsonResponse, Schemas, operations } from "./wire.js";

export type Visibility = Schemas["Visibility"];

/** `"image"` or `"video"` (only image presets can be processed in this version). */
export type AssetKind = Schemas["Kind"];

/**
 * `processing` → `ready` | `failed`. `deleted` appears only in
 * `asset.deleted` webhook payloads.
 */
export type AssetStatus = Asset["status"];

export type Scope = Schemas["Scope"];

/** Free-form metadata attached by your product (≤ 16 KB). Never key-converted. */
export type Metadata = Schemas["Metadata"];

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

/** The source file. `filename` and `contentType` may be `null`. */
export type AssetOriginal = Camelize<Schemas["Original"]>;

/**
 * One stored file. Public renditions carry a permanent `url`; private ones
 * carry a storage `key` to sign with `kadr.signUrl()`.
 */
export type Rendition = Camelize<Schemas["Rendition"]>;

/** Base64 ThumbHash and dominant color (`#rrggbb`). */
export type AssetPlaceholder = Camelize<Schemas["Placeholder"]>;

/** Why processing failed (`asset.error`, `asset.failed` webhook). */
export type AssetError = Camelize<Schemas["AssetError"]>;

export type AssetErrorCode = AssetError["code"];

export type Asset = Camelize<Schemas["Asset"]>;

/** Filters of `GET /v1/assets`. Use `assets.list()` to iterate over all pages. */
export type ListAssetsParams = Camelize<NonNullable<operations["listAssets"]["parameters"]["query"]>>;

/** One page of a cursor-paginated list (`{ items, next_cursor }` on the wire). */
export interface Page<T> {
  items: T[];
  /** Pass as `cursor` to fetch the next page; `null` on the last page. */
  nextCursor: string | null;
}

/**
 * Body of `PATCH /v1/assets/{id}`. `metadata` replaces the stored object.
 * Changing `visibility` is not implemented yet (the server answers
 * `501 not_implemented`); sending the current visibility is accepted.
 */
export type UpdateAssetInput = Camelize<
  operations["updateAsset"]["requestBody"]["content"]["application/json"]
>;

/** Fields of a server-side upload (`POST /v1/assets`, multipart/form-data). */
export interface UploadAssetInput {
  preset: CreateUploadInput["preset"];
  namespace: CreateUploadInput["namespace"];
  visibility: CreateUploadInput["visibility"];
  /** File name stored with the asset (also used as the file part name). */
  filename?: string;
  /** Content-Type of the file part. The server detects the real type from the bytes. */
  contentType?: string;
  metadata?: Metadata;
}

// ---------------------------------------------------------------------------
// Uploads
// ---------------------------------------------------------------------------

/** Body of `POST /v1/uploads`. `size` must be exact; `sha256` enables dedup. */
export type CreateUploadInput = Camelize<Schemas["CreateUploadRequest"]>;

/**
 * Single-request upload ticket: `PUT` the file to `url` with exactly
 * `headers` (Content-Type and Content-Length are signed), then call
 * `uploads.complete(uploadId)`. Video files over 64 MB get a `MULTIPART`
 * ticket instead: PUT each of `parts` (`partSize` bytes per part), collect the
 * `ETag` headers and call `uploads.complete(uploadId, { parts })`.
 */
export type UploadTicket = Camelize<Schemas["UploadTicket"]>;

/**
 * Result of `uploads.create()`.
 *
 * - `kind: "upload"` (HTTP 201): a ticket; the client must upload the file.
 *   The ticket fields are flattened onto the result (`result.uploadId`, ...).
 * - `kind: "asset"` (HTTP 200): the content was deduplicated by `sha256`;
 *   a new ready asset is returned and no upload is needed.
 */
export type CreateUploadResult = ({ kind: "upload" } & UploadTicket) | { kind: "asset"; asset: Asset };

/** A finished part of a multipart upload. */
export interface CompletedPart {
  partNumber: number;
  etag: string;
}

export interface CompleteUploadInput {
  /** Required to complete `MULTIPART` tickets. */
  parts?: CompletedPart[];
  /**
   * How long the server may wait for processing before returning (max 30 s).
   * A number is interpreted as seconds; a string is sent verbatim (e.g. `"5s"`).
   */
  wait?: number | string;
}

// ---------------------------------------------------------------------------
// Presets, namespaces, ping
// ---------------------------------------------------------------------------

/** A preset with the parameters of its current version (`params` is not key-converted). */
export type Preset = Camelize<Schemas["Preset"]>;

/** Usage and limits of a namespace. `null` limits mean unlimited. */
export type NamespaceUsage = Camelize<Schemas["NamespaceUsage"]>;

/**
 * Body of `PUT /v1/namespaces/{ns}/quota`. Replaces **all** limits: an
 * omitted field is stored as `null` (unlimited).
 */
export type NamespaceQuotaInput = Camelize<Schemas["QuotaInput"]>;

/** Result of `GET /v1/ping`: the project and scopes of the API key. */
export type PingResult = Camelize<JsonResponse<"ping", 200>>;

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

/** Adds the `webhook-id` header value (the event id, `evt_…`) to a payload. */
type WithId<E> = E & {
  /** Value of the `webhook-id` header. Identical across retries; deduplicate on it. */
  id: string;
};

export type AssetReadyEvent = WithId<Camelize<Schemas["AssetReadyEvent"]>>;
export type AssetFailedEvent = WithId<Camelize<Schemas["AssetFailedEvent"]>>;
export type AssetUpdatedEvent = WithId<Camelize<Schemas["AssetUpdatedEvent"]>>;
export type AssetDeletedEvent = WithId<Camelize<Schemas["AssetDeletedEvent"]>>;
export type UploadExpiredEvent = WithId<Camelize<Schemas["UploadExpiredEvent"]>>;
export type QuotaWarningEvent = WithId<Camelize<Schemas["QuotaWarningEvent"]>>;
export type QuotaExceededEvent = WithId<Camelize<Schemas["QuotaExceededEvent"]>>;
/** Sent only by the admin console's "Send test" action. */
export type WebhookTestEvent = WithId<Camelize<Schemas["WebhookTestEvent"]>>;

export type AssetEventData = Camelize<Schemas["AssetEventData"]>;
export type QuotaEventData = Camelize<Schemas["QuotaEventData"]>;

/** Every webhook Kadr sends, discriminated by `type`. */
export type WebhookEvent =
  | AssetReadyEvent
  | AssetFailedEvent
  | AssetUpdatedEvent
  | AssetDeletedEvent
  | UploadExpiredEvent
  | QuotaWarningEvent
  | QuotaExceededEvent
  | WebhookTestEvent;

export type WebhookEventType = WebhookEvent["type"];

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/**
 * RFC 9457 problem details as returned by the API (top-level keys camelCased).
 * `errors` maps wire field names (e.g. `content_type`) to messages.
 */
export type ProblemDetails = Partial<Camelize<Schemas["Problem"]>> & Record<string, unknown>;

// ---------------------------------------------------------------------------
// Signed URLs and imports
// ---------------------------------------------------------------------------

/** One entry of `assets.signUrls()`. */
export interface SignUrlItem {
  assetId: string;
  /** Rendition name ("main") or "name.format" ("main.avif"); defaults to the first. */
  rendition?: string;
  /** Lifetime in seconds (default 3600, max 604800). */
  ttl?: number;
  /** Serve as an attachment with this file name. */
  download?: string;
}

export interface SignedUrl {
  url: string;
  expiresAt: string;
}

interface ImportCommon {
  preset: string;
  namespace: string;
  visibility: "public" | "private";
  filename?: string;
  metadata?: Record<string, unknown>;
}

/** Body of `POST /v1/imports/url`. */
export interface ImportUrlInput extends ImportCommon {
  url: string;
}

/** Body of `POST /v1/imports/cloud`. */
export interface ImportCloudInput extends ImportCommon {
  connectionId: string;
  /** Drive file id, or a path for OneDrive and Dropbox. */
  fileId: string;
}
