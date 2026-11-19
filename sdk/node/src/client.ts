import { fromWire, toWire } from "./case.js";
import { KadrError } from "./errors.js";
import {
  HttpClient,
  type FetchLike,
  type IdempotentRequestOptions,
  type RequestOptions,
} from "./http.js";
import { signUrl, type SignUrlOptions, type SigningConfig } from "./signing.js";
import type {
  Asset,
  CompleteUploadInput,
  CreateUploadInput,
  CreateUploadResult,
  ListAssetsParams,
  NamespaceQuotaInput,
  NamespaceUsage,
  Page,
  PingResult,
  Preset,
  UpdateAssetInput,
  ImportCloudInput,
  ImportUrlInput,
  SignedUrl,
  SignUrlItem,
  UploadAssetInput,
  UploadTicket,
} from "./types.js";
import type { ApiPath, PathParamNames } from "./wire.js";
import { Webhooks } from "./webhooks.js";

/** Maximum size of a server-side upload via `POST /v1/assets` (25 MB). */
export const MAX_SERVER_UPLOAD_BYTES = 25 * 1024 * 1024;

export interface KadrOptions {
  /** Kadr API origin, e.g. `http://kadr.internal:8080`. `/v1` is appended unless already present. */
  baseUrl: string;
  /** Project API key (`kd_live_…` / `kd_test_…`). */
  apiKey: string;
  /** Required for `signUrl()`. */
  signing?: SigningConfig;
  /** Per-attempt timeout in milliseconds. Default: 30000. */
  timeoutMs?: number;
  /** Maximum number of retries for retryable requests. Default: 2. */
  maxRetries?: number;
  /** Base delay for exponential backoff in milliseconds. Default: 500. */
  retryBaseDelayMs?: number;
  /** Upper bound for a single retry delay (incl. `Retry-After`) in milliseconds. Default: 30000. */
  maxRetryDelayMs?: number;
  /** Custom fetch implementation (defaults to the global `fetch`). */
  fetch?: FetchLike;
  /** Overrides the `User-Agent` header. */
  userAgent?: string;
}

/** A file for `assets.upload()`. Streams are buffered into memory (max 25 MB). */
export type UploadFile =
  | Blob
  | Uint8Array
  | ArrayBuffer
  | ReadableStream<Uint8Array>
  | AsyncIterable<Uint8Array>;

/**
 * Builds a request path from an OpenAPI path template. Only paths that exist
 * in `api/openapi.yaml` type-check, and every `{param}` must be supplied.
 */
function route<P extends ApiPath>(
  template: P,
  ...params: [PathParamNames<P>] extends [never] ? [] : [Record<PathParamNames<P>, string>]
): string {
  const values: Record<string, string> = params[0] ?? {};
  return template
    .replace(/^\/v1/, "")
    .replace(/\{(\w+)\}/g, (_, name: string) => encodeURIComponent(values[name] ?? ""));
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === "object" && !Array.isArray(v);
}

function unexpected(what: string, body: unknown): KadrError {
  return new KadrError(`Unexpected response body for ${what}`, {
    status: 0,
    code: "invalid_response",
    detail: (typeof body === "string" ? body : (JSON.stringify(body) as string | undefined) ?? "").slice(0, 1000),
  });
}

/** Parses a `{ items: [...], next_cursor? }` list envelope. */
function toPage<T>(what: string, body: unknown): Page<T> {
  if (!isRecord(body) || !Array.isArray(body.items)) throw unexpected(what, body);
  const next = body.next_cursor;
  return {
    items: body.items.map((item) => fromWire<T>(item)),
    nextCursor: typeof next === "string" && next !== "" ? next : null,
  };
}

function formatWait(wait: number | string | undefined): string | undefined {
  if (wait === undefined) return undefined;
  return typeof wait === "number" ? `${wait}s` : wait;
}

async function toBlob(file: UploadFile, contentType: string | undefined): Promise<Blob> {
  const type = contentType ?? "";
  if (file instanceof Blob) {
    return contentType && file.type !== contentType ? new Blob([file], { type }) : file;
  }
  if (file instanceof ArrayBuffer) return new Blob([file], { type });
  if (file instanceof Uint8Array) return new Blob([new Uint8Array(file)], { type });

  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let total = 0;
  const push = (chunk: Uint8Array) => {
    total += chunk.byteLength;
    if (total > MAX_SERVER_UPLOAD_BYTES) {
      throw new KadrError("File exceeds the 25 MB limit for server-side uploads", {
        status: 0,
        code: "too_large",
      });
    }
    chunks.push(new Uint8Array(chunk));
  };

  if (file instanceof ReadableStream) {
    const reader = (file as ReadableStream<Uint8Array>).getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        push(value);
      }
    } catch (err) {
      await reader.cancel().catch(() => undefined);
      throw err;
    } finally {
      reader.releaseLock();
    }
  } else {
    for await (const chunk of file) push(chunk);
  }
  return new Blob(chunks, { type });
}

class Uploads {
  constructor(private readonly http: HttpClient) {}

  /**
   * Creates an upload ticket (`POST /v1/uploads`).
   *
   * Returns `{ kind: "upload", ...ticket }` (201), or `{ kind: "asset", asset }`
   * (200) when `sha256` matched content already stored for the same project,
   * visibility and preset version.
   */
  async create(
    input: CreateUploadInput,
    options: IdempotentRequestOptions = {},
  ): Promise<CreateUploadResult> {
    const res = await this.http.request({
      method: "POST",
      path: route("/v1/uploads"),
      json: input,
      ...options,
    });
    const body = res.body;
    if (res.status === 201 && isRecord(body) && typeof body.upload_id === "string") {
      return { kind: "upload", ...fromWire<UploadTicket>(body) };
    }
    if (res.status === 200 && isRecord(body) && isRecord(body.asset)) {
      return { kind: "asset", asset: fromWire<Asset>(body.asset) };
    }
    throw unexpected("POST /v1/uploads", body);
  }

  /**
   * Completes an upload (`POST /v1/uploads/{id}/complete?wait=…`) after the
   * file was PUT to the ticket URL, and returns the asset (`ready`, `failed`
   * or still `processing`). Idempotent on the server.
   */
  async complete(
    uploadId: string,
    input: CompleteUploadInput & IdempotentRequestOptions = {},
  ): Promise<Asset> {
    const { wait, parts, ...options } = input;
    const res = await this.http.request({
      method: "POST",
      path: route("/v1/uploads/{upload_id}/complete", { upload_id: uploadId }),
      query: { wait: formatWait(wait) },
      ...(parts ? { json: { parts: parts.map((p) => ({ part_number: p.partNumber, etag: p.etag })) } } : {}),
      ...options,
    });
    return fromWire<Asset>(res.body);
  }

  /** Cancels a pending upload (`DELETE /v1/uploads/{id}`). Cancelling twice is a no-op. */
  async abort(uploadId: string, options: RequestOptions = {}): Promise<void> {
    await this.http.request({
      method: "DELETE",
      path: route("/v1/uploads/{upload_id}", { upload_id: uploadId }),
      ...options,
    });
  }
}

class Assets {
  constructor(private readonly http: HttpClient) {}

  /** `GET /v1/assets/{id}` */
  async get(id: string, options: RequestOptions = {}): Promise<Asset> {
    const res = await this.http.request({
      method: "GET",
      path: route("/v1/assets/{asset_id}", { asset_id: id }),
      ...options,
    });
    return fromWire<Asset>(res.body);
  }

  /** Fetches a single page of assets, newest first (`GET /v1/assets`). */
  async listPage(params: ListAssetsParams = {}, options: RequestOptions = {}): Promise<Page<Asset>> {
    const res = await this.http.request({
      method: "GET",
      path: route("/v1/assets"),
      query: { ...params },
      ...options,
    });
    return toPage<Asset>("GET /v1/assets", res.body);
  }

  /**
   * Iterates over all assets matching `params`, following `next_cursor`.
   *
   * ```ts
   * for await (const asset of kadr.assets.list({ namespace: "org_8f2c" })) { … }
   * ```
   */
  async *list(params: ListAssetsParams = {}, options: RequestOptions = {}): AsyncGenerator<Asset, void, undefined> {
    let cursor = params.cursor;
    for (;;) {
      const page = await this.listPage({ ...params, cursor }, options);
      yield* page.items;
      if (!page.nextCursor) return;
      cursor = page.nextCursor;
    }
  }

  /**
   * `PATCH /v1/assets/{id}` — replaces `metadata` and/or changes `visibility`
   * (renditions are copied into the other bucket: keys and URLs change — store
   * the returned asset). Emits one `asset.updated`. Not retried.
   */
  async update(id: string, input: UpdateAssetInput, options: RequestOptions = {}): Promise<Asset> {
    const res = await this.http.request({
      method: "PATCH",
      path: route("/v1/assets/{asset_id}", { asset_id: id }),
      json: input,
      ...options,
    });
    return fromWire<Asset>(res.body);
  }

  /**
   * `DELETE /v1/assets/{id}` — fails with `409 asset_processing` while the
   * asset is processing. Not retried automatically.
   */
  async delete(id: string, options: RequestOptions = {}): Promise<void> {
    await this.http.request({
      method: "DELETE",
      path: route("/v1/assets/{asset_id}", { asset_id: id }),
      ...options,
    });
  }

  /**
   * Server-side upload (`POST /v1/assets`, multipart/form-data) for files up
   * to 25 MB. Streams are buffered into memory first. The server computes
   * the SHA-256 itself (deduplicated content returns a ready asset).
   */
  async upload(
    file: UploadFile,
    input: UploadAssetInput & { wait?: number | string },
    options: IdempotentRequestOptions = {},
  ): Promise<Asset> {
    const blob = await toBlob(file, input.contentType);
    const form = new FormData();
    if (input.preset !== undefined) form.append("preset", input.preset);
    if (input.mode !== undefined) form.append("mode", input.mode);
    form.append("namespace", input.namespace);
    form.append("visibility", input.visibility);
    if (input.filename !== undefined) form.append("filename", input.filename);
    if (input.metadata !== undefined) form.append("metadata", JSON.stringify(input.metadata));
    form.append("file", blob, input.filename ?? "file");

    const res = await this.http.request({
      method: "POST",
      path: route("/v1/assets"),
      query: { wait: formatWait(input.wait) },
      form,
      ...options,
    });
    return fromWire<Asset>(res.body);
  }
}

class Imports {
  constructor(private readonly http: HttpClient) {}

  /** `POST /v1/imports/url` — Kadr downloads the URL (SSRF-safe); returns the processing asset. */
  async url(input: ImportUrlInput, options: IdempotentRequestOptions = {}): Promise<Asset> {
    const res = await this.http.request({ method: "POST", path: route("/v1/imports/url"), json: toWire(input), ...options });
    return fromWire<Asset>(res.body);
  }

  /** `POST /v1/imports/cloud` — imports one file through a cloud connection. */
  async cloud(input: ImportCloudInput, options: IdempotentRequestOptions = {}): Promise<Asset> {
    const res = await this.http.request({ method: "POST", path: route("/v1/imports/cloud"), json: toWire(input), ...options });
    return fromWire<Asset>(res.body);
  }
}

class Presets {
  constructor(private readonly http: HttpClient) {}

  /** `GET /v1/presets` — the project's active presets. */
  async list(options: RequestOptions = {}): Promise<Preset[]> {
    const res = await this.http.request({ method: "GET", path: route("/v1/presets"), ...options });
    return toPage<Preset>("GET /v1/presets", res.body).items;
  }

  /** `GET /v1/presets/{slug}` */
  async get(slug: string, options: RequestOptions = {}): Promise<Preset> {
    const res = await this.http.request({
      method: "GET",
      path: route("/v1/presets/{slug}", { slug }),
      ...options,
    });
    return fromWire<Preset>(res.body);
  }
}

class Namespaces {
  constructor(private readonly http: HttpClient) {}

  /** `GET /v1/namespaces/{ns}/usage` — unknown namespaces report zero usage. */
  async usage(namespace: string, options: RequestOptions = {}): Promise<NamespaceUsage> {
    const res = await this.http.request({
      method: "GET",
      path: route("/v1/namespaces/{namespace}/usage", { namespace }),
      ...options,
    });
    return fromWire<NamespaceUsage>(res.body);
  }

  /**
   * `PUT /v1/namespaces/{ns}/quota` — replaces **all** limits (an omitted
   * field means unlimited) and returns the usage after the change. Not
   * retried automatically. Requires the `namespaces:admin` scope.
   */
  async setQuota(
    namespace: string,
    quota: NamespaceQuotaInput,
    options: RequestOptions = {},
  ): Promise<NamespaceUsage> {
    const res = await this.http.request({
      method: "PUT",
      path: route("/v1/namespaces/{namespace}/quota", { namespace }),
      json: quota,
      ...options,
    });
    return fromWire<NamespaceUsage>(res.body);
  }
}

/** Kadr API client. */
export class Kadr {
  readonly uploads: Uploads;
  readonly assets: Assets;
  readonly presets: Presets;
  readonly imports: Imports;
  readonly namespaces: Namespaces;
  readonly webhooks: Webhooks;
  private readonly http: HttpClient;
  private readonly signing: SigningConfig | undefined;

  constructor(options: KadrOptions) {
    if (!options.baseUrl) throw new TypeError("Kadr: baseUrl is required");
    if (!options.apiKey) throw new TypeError("Kadr: apiKey is required");
    const fetchImpl = options.fetch ?? (globalThis as { fetch?: FetchLike }).fetch;
    if (!fetchImpl) throw new TypeError("Kadr: no global fetch available; pass options.fetch");

    this.http = new HttpClient({
      baseUrl: options.baseUrl,
      apiKey: options.apiKey,
      timeoutMs: options.timeoutMs ?? 30_000,
      maxRetries: options.maxRetries ?? 2,
      retryBaseDelayMs: options.retryBaseDelayMs ?? 500,
      maxRetryDelayMs: options.maxRetryDelayMs ?? 30_000,
      fetch: fetchImpl,
      userAgent: options.userAgent,
    });
    this.signing = options.signing;
    this.uploads = new Uploads(this.http);
    this.assets = new Assets(this.http);
    this.presets = new Presets(this.http);
    this.imports = new Imports(this.http);
    this.namespaces = new Namespaces(this.http);
    this.webhooks = new Webhooks();
  }

  /** `GET /v1/ping` — verifies the API key and returns its project and scopes. */
  async ping(options: RequestOptions = {}): Promise<PingResult> {
    const res = await this.http.request({ method: "GET", path: route("/v1/ping"), ...options });
    return fromWire<PingResult>(res.body);
  }

  /**
   * Signs private rendition URLs on the server (`POST /v1/signed-urls`) with
   * the project's active signing key. Needs the `urls:sign` scope.
   */
  async signUrls(items: SignUrlItem[], options: IdempotentRequestOptions = {}): Promise<SignedUrl[]> {
    const res = await this.http.request({
      method: "POST",
      path: route("/v1/signed-urls"),
      json: { items: items.map((i) => toWire(i)) },
      ...options,
    });
    const body = res.body;
    if (!isRecord(body) || !Array.isArray(body.items)) throw unexpected("POST /v1/signed-urls", body);
    return body.items.map((i) => fromWire<SignedUrl>(i));
  }

  /**
   * Signs a private storage key locally (no network call) and returns the
   * delivery URL. Requires `signing` in the constructor options.
   */
  signUrl(key: string, options: SignUrlOptions): string {
    if (!this.signing) throw new TypeError("Kadr: signing config is required for signUrl()");
    return signUrl(this.signing, key, options);
  }
}

export type { Uploads, Assets, Presets, Namespaces };
