import { KadrUploadError, throwIfAborted } from "./errors.js";
import { preResizeImage } from "./resize.js";
import { withRetry } from "./retry.js";
import { xhrPut } from "./xhr.js";
import type {
  CompletedPart,
  MultipartPart,
  MultipartUploadTicket,
  PutUploadTicket,
  UploadFileOptions,
  UploadOptions,
  UploadTicket,
} from "./types.js";

export const DEFAULT_CONCURRENCY = 4;

function retryOpts(o: UploadOptions, onRetry?: () => void) {
  return {
    ...(o.retries === undefined ? {} : { retries: o.retries }),
    ...(o.baseDelayMs === undefined ? {} : { baseDelayMs: o.baseDelayMs }),
    ...(o.maxDelayMs === undefined ? {} : { maxDelayMs: o.maxDelayMs }),
    signal: o.signal,
    sleep: o.sleep,
    onRetry,
  };
}

/** PUTs `body` to a single-request ticket. */
export async function putToTicket(body: Blob, ticket: PutUploadTicket, opts: UploadOptions = {}): Promise<void> {
  const total = body.size;
  const report = (loaded: number) => {
    opts.onProgress?.({ loaded, total, fraction: total === 0 ? 1 : Math.min(1, loaded / total) });
  };
  await withRetry(
    () =>
      xhrPut(ticket.url, body, {
        headers: ticket.headers,
        signal: opts.signal,
        timeoutMs: opts.timeoutMs,
        createXhr: opts.createXhr,
        onProgress: (loaded) => {
          report(loaded);
        },
      }),
    retryOpts(opts, () => {
      report(0);
    }),
  );
}

const MAX_PART_URL_REFRESHES = 2;

/** Uploads all parts with bounded parallelism and returns `[{part_number, etag}]` sorted by part number. */
export async function putMultipart(
  body: Blob,
  ticket: MultipartUploadTicket,
  opts: UploadOptions = {},
): Promise<CompletedPart[]> {
  const { part_size: partSize, parts } = ticket;
  if (!(partSize > 0)) throw new KadrUploadError("multipart ticket has an invalid part_size");
  const expected = Math.max(1, Math.ceil(body.size / partSize));
  const doneMap = new Map<number, CompletedPart>();
  const loadedByPart = new Map<number, number>();

  for (const cp of opts.completedParts ?? []) {
    if (!Number.isInteger(cp.part_number) || cp.part_number < 1 || cp.part_number > expected) {
      throw new KadrUploadError(`completedParts contains invalid part_number ${cp.part_number} (expected 1..${expected})`, {
        partNumber: cp.part_number,
      });
    }
    if (!cp.etag) continue;
    doneMap.set(cp.part_number, { part_number: cp.part_number, etag: cp.etag });
    const start = (cp.part_number - 1) * partSize;
    const len = Math.max(0, Math.min(body.size, start + partSize) - start);
    loadedByPart.set(cp.part_number, len);
  }

  const urlMap = new Map<number, MultipartPart>();
  for (const p of parts) urlMap.set(p.part_number, p);

  if (doneMap.size === 0 && parts.length !== expected) {
    throw new KadrUploadError(`multipart ticket has ${parts.length} parts, expected ${expected} for ${body.size} bytes`);
  }
  const todo: number[] = [];
  for (let n = 1; n <= expected; n++) {
    if (!doneMap.has(n)) {
      if (!urlMap.has(n)) {
        throw new KadrUploadError(`multipart ticket is missing part ${n} of ${expected}`, { partNumber: n });
      }
      todo.push(n);
    }
  }

  const concurrency = Math.max(1, Math.floor(opts.concurrency ?? DEFAULT_CONCURRENCY));
  const total = body.size;
  const report = (partNumber?: number) => {
    let loaded = 0;
    for (const v of loadedByPart.values()) loaded += v;
    opts.onProgress?.({
      loaded,
      total,
      fraction: total === 0 ? 1 : Math.min(1, loaded / total),
      ...(partNumber === undefined ? {} : { partNumber }),
    });
  };

  if (doneMap.size > 0) {
    report();
  }
  if (todo.length === 0) {
    throwIfAborted(opts.signal);
    return Array.from(doneMap.values()).sort((a, b) => a.part_number - b.part_number);
  }

  const ctl = new AbortController();
  const onOuterAbort = () => {
    ctl.abort();
  };
  opts.signal?.addEventListener("abort", onOuterAbort, { once: true });
  if (opts.signal?.aborted) ctl.abort();

  const refreshFn = opts.refreshParts;
  let inFlightRefresh: Promise<void> | null = null;
  const ensureRefreshed = async (failedPart: number): Promise<void> => {
    if (!refreshFn) return;
    if (!inFlightRefresh) {
      const remaining: number[] = [];
      for (const n of todo) {
        if (!doneMap.has(n)) remaining.push(n);
      }
      if (remaining.length === 0) remaining.push(failedPart);
      inFlightRefresh = (async () => {
        try {
          const fresh = await refreshFn(ticket.upload_id, remaining);
          for (const fp of fresh) urlMap.set(fp.part_number, fp);
        } finally {
          inFlightRefresh = null;
        }
      })();
    }
    await inFlightRefresh;
  };

  let next = 0;
  let firstError: unknown;
  const worker = async () => {
    while (!ctl.signal.aborted) {
      const idx = next++;
      const partNumber = todo[idx];
      if (partNumber === undefined) return;
      const start = (partNumber - 1) * partSize;
      const chunk = body.slice(start, Math.min(body.size, start + partSize));
      let refreshes = 0;
      try {
        for (;;) {
          const part = urlMap.get(partNumber);
          if (!part) {
            throw new KadrUploadError(`multipart ticket is missing refreshed URL for part ${partNumber}`, {
              partNumber,
            });
          }
          try {
            const res = await withRetry(
              () =>
                xhrPut(part.url, chunk, {
                  headers: part.headers,
                  signal: ctl.signal,
                  timeoutMs: opts.timeoutMs,
                  createXhr: opts.createXhr,
                  partNumber,
                  onProgress: (loaded) => {
                    loadedByPart.set(partNumber, loaded);
                    report(partNumber);
                  },
                }),
              retryOpts({ ...opts, signal: ctl.signal }, () => {
                loadedByPart.set(partNumber, 0);
                report(partNumber);
              }),
            );
            if (!res.etag) {
              throw new KadrUploadError(
                `part ${partNumber}: no ETag in the response; expose the ETag header in the bucket CORS configuration`,
                { partNumber },
              );
            }
            const completed: CompletedPart = { part_number: partNumber, etag: res.etag };
            doneMap.set(partNumber, completed);
            opts.onPartComplete?.(completed);
            break;
          } catch (err) {
            if (err instanceof KadrUploadError && err.status === 403 && refreshFn && refreshes < MAX_PART_URL_REFRESHES) {
              refreshes++;
              loadedByPart.set(partNumber, 0);
              report(partNumber);
              await ensureRefreshed(partNumber);
              if (!urlMap.get(partNumber)) throw err;
              continue;
            }
            throw err;
          }
        }
      } catch (err) {
        firstError ??= err;
        ctl.abort();
        return;
      }
    }
  };

  try {
    await Promise.all(Array.from({ length: Math.min(concurrency, todo.length) }, worker));
  } finally {
    opts.signal?.removeEventListener("abort", onOuterAbort);
  }
  if (firstError !== undefined) throw firstError as Error;
  throwIfAborted(opts.signal);
  return Array.from(doneMap.values()).sort((a, b) => a.part_number - b.part_number);
}

/** Uploads `body` according to the ticket kind. Returns the parts to pass to `complete` (undefined for PUT). */
export async function uploadToTicket(
  body: Blob,
  ticket: UploadTicket,
  opts: UploadOptions = {},
): Promise<CompletedPart[] | undefined> {
  if (ticket.method === "MULTIPART") return putMultipart(body, ticket, opts);
  await putToTicket(body, ticket, opts);
  return undefined;
}

/**
 * Full browser flow: optional pre-resize, ticket from your backend, upload to
 * storage, then `complete` (again via your backend). The browser never holds an API key.
 */
export async function uploadFile<TAsset>(
  file: File | Blob,
  opts: UploadFileOptions<TAsset>,
): Promise<TAsset> {
  throwIfAborted(opts.signal);
  const name = "name" in file && typeof file.name === "string" && file.name ? file.name : "upload";
  let body: Blob = file;
  let filename = name;
  let contentType = file.type || "application/octet-stream";
  if (opts.preResize) {
    const r = await preResizeImage(file, name, opts.preResize, opts.signal);
    body = r.blob;
    filename = r.filename;
    contentType = r.contentType || contentType;
  }
  throwIfAborted(opts.signal);
  const ticket = await opts.createTicket({ filename, content_type: contentType, size: body.size });
  throwIfAborted(opts.signal);
  const parts = await uploadToTicket(body, ticket, opts);
  throwIfAborted(opts.signal);
  return opts.complete(ticket.upload_id, parts);
}
