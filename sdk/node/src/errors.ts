import { snakeToCamel } from "./case.js";
import type { ProblemDetails } from "./types.js";

export interface KadrErrorInit {
  status: number;
  code: string;
  title?: string | undefined;
  detail?: string | undefined;
  type?: string | undefined;
  instance?: string | undefined;
  errors?: unknown[] | undefined;
  requestId?: string | undefined;
  problem?: ProblemDetails | undefined;
  cause?: unknown;
}

/**
 * Error thrown for any failed API call.
 *
 * For HTTP errors the fields come from the RFC 9457 `application/problem+json`
 * body. Non-JSON error bodies still produce a `KadrError` (with
 * `code: "http_<status>"` and the body text as `detail`).
 * Transport failures use `status: 0` and one of the subclasses below.
 */
export class KadrError extends Error {
  /** HTTP status, or `0` when no response was received. */
  readonly status: number;
  /** Machine-readable error code (e.g. `quota_exceeded`, `network_error`, `timeout`). */
  readonly code: string;
  readonly title: string | undefined;
  readonly detail: string | undefined;
  /** Problem type URI. */
  readonly type: string | undefined;
  readonly instance: string | undefined;
  /** Field-level validation errors, passed through as sent by the server. */
  readonly errors: unknown[] | undefined;
  /** Value of the `x-request-id` (or `request-id`) response header, when present. */
  readonly requestId: string | undefined;
  /** The full problem document (camelCased top-level keys), when the body was JSON. */
  readonly problem: ProblemDetails | undefined;

  constructor(message: string, init: KadrErrorInit) {
    super(message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = "KadrError";
    this.status = init.status;
    this.code = init.code;
    this.title = init.title;
    this.detail = init.detail;
    this.type = init.type;
    this.instance = init.instance;
    this.errors = init.errors;
    this.requestId = init.requestId;
    this.problem = init.problem;
  }
}

/** The request could not be sent or no response was received (DNS, connection reset, ...). */
export class KadrNetworkError extends KadrError {
  constructor(message: string, cause?: unknown) {
    super(message, { status: 0, code: "network_error", cause });
    this.name = "KadrNetworkError";
  }
}

/** The request did not complete within `timeoutMs`. */
export class KadrTimeoutError extends KadrError {
  constructor(timeoutMs: number, cause?: unknown) {
    super(`Request timed out after ${timeoutMs} ms`, { status: 0, code: "timeout", cause });
    this.name = "KadrTimeoutError";
  }
}

/** The request was aborted by the caller's `AbortSignal`. Never retried. */
export class KadrAbortError extends KadrError {
  constructor(cause?: unknown) {
    super("Request was aborted", { status: 0, code: "aborted", cause });
    this.name = "KadrAbortError";
  }
}

/** Thrown by webhook verification when the signature, timestamp or payload is invalid. */
export class WebhookVerificationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WebhookVerificationError";
  }
}

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** Builds a `KadrError` from an HTTP error response. Never throws. */
export async function errorFromResponse(res: Response): Promise<KadrError> {
  const requestId =
    res.headers.get("x-request-id") ?? res.headers.get("request-id") ?? undefined;
  let text = "";
  try {
    text = await res.text();
  } catch {
    // ignore body read failures
  }

  let body: unknown;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = undefined;
    }
  }

  if (body !== null && typeof body === "object" && !Array.isArray(body)) {
    const raw = body as Record<string, unknown>;
    const problem: ProblemDetails = {};
    for (const [k, v] of Object.entries(raw)) {
      problem[snakeToCamel(k)] = v;
    }
    const code = str(raw.code) ?? `http_${res.status}`;
    const title = str(raw.title);
    const detail = str(raw.detail);
    const message = [title ?? `HTTP ${res.status}`, detail].filter(Boolean).join(": ");
    return new KadrError(`${message} (${code})`, {
      status: res.status,
      code,
      title,
      detail,
      type: str(raw.type),
      instance: str(raw.instance),
      errors: Array.isArray(raw.errors) ? (raw.errors as unknown[]) : undefined,
      requestId,
      problem,
    });
  }

  const code = `http_${res.status}`;
  const detail = text ? text.slice(0, 1000) : undefined;
  const title = res.statusText || undefined;
  return new KadrError(`HTTP ${res.status}${title ? ` ${title}` : ""} (${code})`, {
    status: res.status,
    code,
    title,
    detail,
    requestId,
  });
}
