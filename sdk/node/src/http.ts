import {
  KadrAbortError,
  KadrError,
  KadrNetworkError,
  KadrTimeoutError,
  errorFromResponse,
} from "./errors.js";
import { toWire } from "./case.js";

export const SDK_VERSION = "0.1.0";

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export interface HttpClientOptions {
  baseUrl: string;
  apiKey: string;
  timeoutMs: number;
  maxRetries: number;
  retryBaseDelayMs: number;
  maxRetryDelayMs: number;
  fetch: FetchLike;
  userAgent?: string | undefined;
}

/** Per-call options accepted by every SDK method. */
export interface RequestOptions {
  /** Abort the request (and any pending retries). */
  signal?: AbortSignal;
  /** Overrides the client-wide timeout for this call (per attempt). */
  timeoutMs?: number;
}

/** Options for POST calls, which accept an idempotency key. */
export interface IdempotentRequestOptions extends RequestOptions {
  /**
   * Sent as the `Idempotency-Key` header (kept by the server for 24 h).
   * A POST is retried automatically only when this is set.
   */
  idempotencyKey?: string;
}

export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export interface RequestSpec {
  method: HttpMethod;
  /** Path relative to `/v1`, starting with `/`. */
  path: string;
  query?: Record<string, string | number | boolean | undefined | null>;
  /** JSON body in SDK (camelCase) form; converted to snake_case. */
  json?: unknown;
  /** multipart/form-data body; sent as-is. */
  form?: FormData;
  idempotencyKey?: string | undefined;
  signal?: AbortSignal | undefined;
  timeoutMs?: number | undefined;
}

export interface RawResponse {
  status: number;
  headers: Headers;
  /** Parsed JSON body (snake_case), the raw text for non-JSON bodies, or `undefined` when empty. */
  body: unknown;
}

/** Normalizes a base URL to `<origin>[/prefix]/v1` without a trailing slash. */
export function normalizeBaseUrl(baseUrl: string): string {
  let url = baseUrl.trim().replace(/\/+$/, "");
  if (!/\/v1$/.test(url)) url += "/v1";
  return url;
}

/** Parses `Retry-After` (delta-seconds or HTTP-date) into milliseconds. */
export function parseRetryAfter(value: string | null, now = Date.now()): number | undefined {
  if (value === null) return undefined;
  const trimmed = value.trim();
  if (trimmed === "") return undefined;
  if (/^\d+(\.\d+)?$/.test(trimmed)) return Math.round(Number(trimmed) * 1000);
  const date = Date.parse(trimmed);
  if (Number.isNaN(date)) return undefined;
  return Math.max(0, date - now);
}

const sleep = (ms: number) =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, ms);
  });

export class HttpClient {
  readonly baseUrl: string;
  private readonly opts: HttpClientOptions;

  constructor(opts: HttpClientOptions) {
    this.opts = opts;
    this.baseUrl = normalizeBaseUrl(opts.baseUrl);
  }

  /**
   * Retry policy: only GET requests and POST requests carrying an
   * `Idempotency-Key` are retried (on network errors, 429, 5xx except 501,
   * and `409 idempotency_in_progress`). PUT/PATCH/DELETE are never retried.
   */
  private isRetryable(spec: RequestSpec): boolean {
    return spec.method === "GET" || (spec.method === "POST" && !!spec.idempotencyKey);
  }

  private backoff(attempt: number): number {
    const exp = Math.min(this.opts.retryBaseDelayMs * 2 ** attempt, this.opts.maxRetryDelayMs);
    // "Equal jitter": half fixed, half random.
    return exp / 2 + Math.random() * (exp / 2);
  }

  buildUrl(spec: Pick<RequestSpec, "path" | "query">): string {
    let url = this.baseUrl + spec.path;
    if (spec.query) {
      const qs = new URLSearchParams();
      for (const [k, v] of Object.entries(spec.query)) {
        if (v === undefined || v === null) continue;
        qs.append(k, String(v));
      }
      const s = qs.toString();
      if (s) url += `?${s}`;
    }
    return url;
  }

  async request(spec: RequestSpec): Promise<RawResponse> {
    const url = this.buildUrl(spec);
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.opts.apiKey}`,
      Accept: "application/json, application/problem+json",
      "User-Agent": this.opts.userAgent ?? `kadr-node/${SDK_VERSION}`,
    };
    let body: RequestInit["body"];
    if (spec.form) {
      body = spec.form; // fetch sets the multipart boundary
    } else if (spec.json !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(toWire(spec.json));
    }
    if (spec.idempotencyKey) headers["Idempotency-Key"] = spec.idempotencyKey;

    const retryable = this.isRetryable(spec);
    const maxRetries = retryable ? Math.max(0, this.opts.maxRetries) : 0;
    const timeoutMs = spec.timeoutMs ?? this.opts.timeoutMs;

    for (let attempt = 0; ; attempt++) {
      if (spec.signal?.aborted) throw new KadrAbortError(spec.signal.reason);
      let res: RawResponse | Response;
      try {
        res = await this.attempt(url, { method: spec.method, headers, body }, timeoutMs, spec.signal);
      } catch (err) {
        if (err instanceof KadrAbortError || attempt >= maxRetries) throw err;
        await sleep(this.backoff(attempt));
        continue;
      }
      if (!(res instanceof Response)) return res;

      // Error response.
      if (res.status === 409 && spec.idempotencyKey) {
        // The first request with this key is still running: wait and replay.
        const err = await errorFromResponse(res);
        if (err.code !== "idempotency_in_progress" || attempt >= maxRetries) throw err;
        await sleep(this.backoff(attempt));
        continue;
      }
      const transient = res.status === 429 || (res.status >= 500 && res.status !== 501);
      if (transient && attempt < maxRetries) {
        const retryAfter = parseRetryAfter(res.headers.get("retry-after"));
        const delay =
          retryAfter !== undefined
            ? Math.min(retryAfter, this.opts.maxRetryDelayMs)
            : this.backoff(attempt);
        await res.body?.cancel().catch(() => undefined);
        await sleep(delay);
        continue;
      }
      throw await errorFromResponse(res);
    }
  }

  /**
   * Performs one attempt. Resolves with a parsed `RawResponse` on 2xx, with
   * the unconsumed `Response` on an HTTP error, and rejects with a transport
   * error otherwise.
   */
  private async attempt(
    url: string,
    init: RequestInit,
    timeoutMs: number,
    userSignal: AbortSignal | undefined,
  ): Promise<RawResponse | Response> {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      controller.abort();
    }, timeoutMs);
    const onAbort = () => {
      controller.abort();
    };
    userSignal?.addEventListener("abort", onAbort, { once: true });

    try {
      const res = await this.opts.fetch(url, { ...init, signal: controller.signal });
      if (!res.ok) return res;
      const text = await res.text();
      let body: unknown = undefined;
      if (text) {
        const ct = res.headers.get("content-type") ?? "";
        if (ct.includes("json")) {
          try {
            body = JSON.parse(text);
          } catch (err) {
            throw new KadrError("Invalid JSON in response body", {
              status: res.status,
              code: "invalid_response",
              detail: text.slice(0, 1000),
              requestId: res.headers.get("x-request-id") ?? undefined,
              cause: err,
            });
          }
        } else {
          body = text;
        }
      }
      return { status: res.status, headers: res.headers, body };
    } catch (err) {
      if (err instanceof KadrError) throw err;
      if (userSignal?.aborted) throw new KadrAbortError(userSignal.reason ?? err);
      if (controller.signal.aborted) throw new KadrTimeoutError(timeoutMs, err);
      const message = err instanceof Error ? err.message : String(err);
      throw new KadrNetworkError(`Network error: ${message}`, err);
    } finally {
      clearTimeout(timer);
      userSignal?.removeEventListener("abort", onAbort);
    }
  }
}
