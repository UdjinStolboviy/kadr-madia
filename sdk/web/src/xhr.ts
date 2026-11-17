import { KadrAbortError, KadrUploadError } from "./errors.js";

export interface PutResult {
  status: number;
  /** Value of the `ETag` response header (as sent, usually quoted), or null if not readable. */
  etag: string | null;
}

export interface PutOptions {
  headers?: Record<string, string> | undefined;
  signal?: AbortSignal | undefined;
  timeoutMs?: number | undefined;
  onProgress?: ((loaded: number, total: number) => void) | undefined;
  createXhr?: (() => XMLHttpRequest) | undefined;
  partNumber?: number | undefined;
}

// Browsers forbid setting these; the body determines them.
const FORBIDDEN = new Set(["content-length", "host"]);

/** One PUT attempt via XHR (fetch cannot report upload progress). */
export function xhrPut(url: string, body: Blob, opts: PutOptions = {}): Promise<PutResult> {
  return new Promise<PutResult>((resolve, reject) => {
    if (opts.signal?.aborted) {
      reject(new KadrAbortError());
      return;
    }
    const xhr = (opts.createXhr ?? (() => new XMLHttpRequest()))();
    const part = opts.partNumber;
    const detail = part === undefined ? "" : ` (part ${part})`;
    const fail = (msg: string, status: number | undefined, retryable: boolean) =>
      new KadrUploadError(msg + detail, { ...(status === undefined ? {} : { status }), retryable, ...(part === undefined ? {} : { partNumber: part }) });

    const onAbortSignal = () => {
      xhr.abort();
    };
    const cleanup = () => {
      opts.signal?.removeEventListener("abort", onAbortSignal);
    };
    opts.signal?.addEventListener("abort", onAbortSignal, { once: true });

    xhr.open("PUT", url, true);
    for (const [k, v] of Object.entries(opts.headers ?? {})) {
      if (!FORBIDDEN.has(k.toLowerCase())) xhr.setRequestHeader(k, v);
    }
    if (opts.timeoutMs) xhr.timeout = opts.timeoutMs;

    xhr.upload.onprogress = (e: ProgressEvent) => {
      opts.onProgress?.(e.lengthComputable ? e.loaded : 0, e.lengthComputable ? e.total : body.size);
    };
    xhr.onload = () => {
      cleanup();
      const s = xhr.status;
      if (s >= 200 && s < 300) {
        opts.onProgress?.(body.size, body.size);
        resolve({ status: s, etag: xhr.getResponseHeader("ETag") });
      } else {
        reject(fail(`storage responded ${s}`, s, s === 408 || s === 429 || s >= 500));
      }
    };
    xhr.onerror = () => {
      cleanup();
      reject(fail("network error", undefined, true));
    };
    xhr.ontimeout = () => {
      cleanup();
      reject(fail("upload timed out", undefined, true));
    };
    xhr.onabort = () => {
      cleanup();
      reject(new KadrAbortError());
    };
    xhr.send(body);
  });
}
