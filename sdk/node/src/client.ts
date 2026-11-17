import { fromWire } from "./case.js";
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
  NamespaceQuota,
  NamespaceUsage,
  Page,
  Preset,
  ReprocessAssetInput,
  UpdateAssetInput,
  UploadAssetInput,
  UploadPartUrl,
  UploadTicket,
} from "./types.js";
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

const seg = encodeURIComponent;

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === "object" && !Array.isArray(v);
}

/** Accepts either a bare asset or an `{ asset: {...} }` wrapper. */
function toAsset(body: unknown): Asset {
  if (isRecord(body) && isRecord(body.asset) && !("id" in body)) return fromWire<Asset>(body.asset);
  return fromWire<Asset>(body);
}

/** Accepts `{ data: [...] }`, `{ items: [...] }` or a bare array. */
function toList(body: unknown): unknown[] {
  if (Array.isArray(body)) return body;
  if (isRecord(body)) {
    if (Array.isArray(body.data)) return body.data;
    if (Array.isArray(body.items)) return body.items;
  }
  return [];
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
   * Returns `{ kind: "upload", ...ticket }` (201) or `{ kind: "asset", asset }`
   * when the file was deduplicated by `sha256` (200).
   */
  async create(
    input: CreateUploadInput,
    options: IdempotentRequestOptions = {},
  ): Promise<CreateUploadResult> {
    const res = await this.http.request({
      method: "POST",
      path: "/uploads",
      json: input,
      ...options,
    });
    const body = res.body;
    const looksLikeTicket = isRecord(body) && typeof body.upload_id === "string";
    if (res.status === 201 || looksLikeTicket) {
      return { kind: "upload", ...fromWire<UploadTicket>(body) };
    }
    return { kind: "asset", asset: toAsset(body) };
  }

  /** Requests more multipart part URLs (`POST /v1/uploads/{id}/parts`). */
  async parts(
    uploadId: string,
    partNumbers: number[],
    options: IdempotentRequestOptions = {},
  ): Promise<UploadPartUrl[]> {
    const res = await this.http.request({
      method: "POST",
      path: `/uploads/${seg(uploadId)}/parts`,
      json: { partNumbers },
      ...options,
    });
    const body = fromWire<{ parts?: UploadPartUrl[] }>(res.body);
    return body.parts ?? [];
  }

  /**
   * Completes an upload (`POST /v1/uploads/{id}/complete?wait=…`) and returns
   * the asset (`status: "ready"` or `"processing"`).
   */
  async complete(
    uploadId: string,
    input: CompleteUploadInput & IdempotentRequestOptions = {},
  ): Promise<Asset> {
    const { parts, wait, ...options } = input;
    const res = await this.http.request({
      method: "POST",
      path: `/uploads/${seg(uploadId)}/complete`,
      query: { wait: formatWait(wait) },
      json: parts ? { parts } : {},
      ...options,
    });
    return toAsset(res.body);
  }

  /** Cancels an upload (`DELETE /v1/uploads/{id}`). */
  async abort(uploadId: string, options: RequestOptions = {}): Promise<void> {
    await this.http.request({ method: "DELETE", path: `/uploads/${seg(uploadId)}`, ...options });
  }
}

class Assets {
  constructor(private readonly http: HttpClient) {}

  /** `GET /v1/assets/{id}` */
  async get(id: string, options: RequestOptions = {}): Promise<Asset> {
    const res = await this.http.request({ method: "GET", path: `/assets/${seg(id)}`, ...options });
    return toAsset(res.body);
  }

  /** Fetches a single page of assets (`GET /v1/assets`). */
  async listPage(params: ListAssetsParams = {}, options: RequestOptions = {}): Promise<Page<Asset>> {
    const res = await this.http.request({
      method: "GET",
      path: "/assets",
      query: { ...params },
      ...options,
    });
    const nextCursor =
      isRecord(res.body) && typeof res.body.next_cursor === "string" && res.body.next_cursor !== ""
        ? res.body.next_cursor
        : null;
    return { data: toList(res.body).map((a) => fromWire<Asset>(a)), nextCursor };
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
      yield* page.data;
      if (!page.nextCursor) return;
      cursor = page.nextCursor;
    }
  }

  /** `PATCH /v1/assets/{id}` — not retried automatically. */
  async update(id: string, input: UpdateAssetInput, options: RequestOptions = {}): Promise<Asset> {
    const res = await this.http.request({
      method: "PATCH",
      path: `/assets/${seg(id)}`,
      json: input,
      ...options,
    });
    return toAsset(res.body);
  }

  /** `DELETE /v1/assets/{id}` — not retried automatically. */
  async delete(id: string, options: RequestOptions = {}): Promise<void> {
    await this.http.request({ method: "DELETE", path: `/assets/${seg(id)}`, ...options });
  }

  /** `POST /v1/assets/{id}/reprocess` */
  async reprocess(
    id: string,
    input: ReprocessAssetInput = {},
    options: IdempotentRequestOptions = {},
  ): Promise<Asset> {
    const res = await this.http.request({
      method: "POST",
      path: `/assets/${seg(id)}/reprocess`,
      json: input,
      ...options,
    });
    return toAsset(res.body);
  }

  /**
   * Server-side upload (`POST /v1/assets`, multipart/form-data) for files up
   * to 25 MB. Streams are buffered into memory first.
   */
  async upload(
    file: UploadFile,
    input: UploadAssetInput,
    options: IdempotentRequestOptions = {},
  ): Promise<Asset> {
    const blob = await toBlob(file, input.contentType);
    const form = new FormData();
    form.append("preset", input.preset);
    form.append("namespace", input.namespace);
    form.append("visibility", input.visibility);
    if (input.filename !== undefined) form.append("filename", input.filename);
    if (input.contentType !== undefined) form.append("content_type", input.contentType);
    if (input.sha256 !== undefined) form.append("sha256", input.sha256);
    if (input.metadata !== undefined) form.append("metadata", JSON.stringify(input.metadata));
    form.append("file", blob, input.filename ?? "file");

    const res = await this.http.request({ method: "POST", path: "/assets", form, ...options });
    return toAsset(res.body);
  }
}

class Presets {
  constructor(private readonly http: HttpClient) {}

  /** `GET /v1/presets` */
  async list(options: RequestOptions = {}): Promise<Preset[]> {
    const res = await this.http.request({ method: "GET", path: "/presets", ...options });
    return toList(res.body).map((p) => fromWire<Preset>(p));
  }

  /** `GET /v1/presets/{slug}` */
  async get(slug: string, options: RequestOptions = {}): Promise<Preset> {
    const res = await this.http.request({ method: "GET", path: `/presets/${seg(slug)}`, ...options });
    return fromWire<Preset>(res.body);
  }
}

class Namespaces {
  constructor(private readonly http: HttpClient) {}

  /** `GET /v1/namespaces/{ns}/usage` */
  async usage(namespace: string, options: RequestOptions = {}): Promise<NamespaceUsage> {
    const res = await this.http.request({
      method: "GET",
      path: `/namespaces/${seg(namespace)}/usage`,
      ...options,
    });
    return fromWire<NamespaceUsage>(res.body);
  }

  /** `PUT /v1/namespaces/{ns}/quota` — not retried automatically. */
  async setQuota(
    namespace: string,
    quota: NamespaceQuota,
    options: RequestOptions = {},
  ): Promise<NamespaceQuota> {
    const res = await this.http.request({
      method: "PUT",
      path: `/namespaces/${seg(namespace)}/quota`,
      json: quota,
      ...options,
    });
    return res.body === undefined ? quota : fromWire<NamespaceQuota>(res.body);
  }

  /**
   * `DELETE /v1/namespaces/{ns}` — deletes all namespace data asynchronously;
   * a `namespace.purged` webhook follows. Not retried automatically.
   */
  async purge(namespace: string, options: RequestOptions = {}): Promise<void> {
    await this.http.request({ method: "DELETE", path: `/namespaces/${seg(namespace)}`, ...options });
  }
}

/** Kadr API client. */
export class Kadr {
  readonly uploads: Uploads;
  readonly assets: Assets;
  readonly presets: Presets;
  readonly namespaces: Namespaces;
  readonly webhooks: Webhooks;
  private readonly signing: SigningConfig | undefined;

  constructor(options: KadrOptions) {
    if (!options.baseUrl) throw new TypeError("Kadr: baseUrl is required");
    if (!options.apiKey) throw new TypeError("Kadr: apiKey is required");
    const fetchImpl = options.fetch ?? (globalThis as { fetch?: FetchLike }).fetch;
    if (!fetchImpl) throw new TypeError("Kadr: no global fetch available; pass options.fetch");

    const http = new HttpClient({
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
    this.uploads = new Uploads(http);
    this.assets = new Assets(http);
    this.presets = new Presets(http);
    this.namespaces = new Namespaces(http);
    this.webhooks = new Webhooks();
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
