import { KadrUploadError, throwIfAborted } from "./errors.js";
import { preResizeImage } from "./resize.js";
import { withRetry } from "./retry.js";
import { xhrPut } from "./xhr.js";
import type {
  CompletedPart,
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

/** Uploads all parts with bounded parallelism and returns `[{part_number, etag}]` sorted by part number. */
export async function putMultipart(
  body: Blob,
  ticket: MultipartUploadTicket,
  opts: UploadOptions = {},
): Promise<CompletedPart[]> {
  const { part_size: partSize, parts } = ticket;
  if (!(partSize > 0)) throw new KadrUploadError("multipart ticket has an invalid part_size");
  const expected = Math.max(1, Math.ceil(body.size / partSize));
  if (parts.length !== expected) {
    throw new KadrUploadError(`multipart ticket has ${parts.length} parts, expected ${expected} for ${body.size} bytes`);
  }
  const concurrency = Math.max(1, Math.floor(opts.concurrency ?? DEFAULT_CONCURRENCY));
  const total = body.size;
  const loadedByPart = new Map<number, number>();
  const report = (partNumber: number) => {
    let loaded = 0;
    for (const v of loadedByPart.values()) loaded += v;
    opts.onProgress?.({ loaded, total, fraction: total === 0 ? 1 : Math.min(1, loaded / total), partNumber });
  };

  const ordered = [...parts].sort((a, b) => a.part_number - b.part_number);
  const results: CompletedPart[] = [];
  const ctl = new AbortController();
  const onOuterAbort = () => {
    ctl.abort();
  };
  opts.signal?.addEventListener("abort", onOuterAbort, { once: true });
  if (opts.signal?.aborted) ctl.abort();

  let next = 0;
  let firstError: unknown;
  const worker = async () => {
    while (!ctl.signal.aborted) {
      const idx = next++;
      const part = ordered[idx];
      if (!part) return;
      const start = (part.part_number - 1) * partSize;
      const chunk = body.slice(start, Math.min(body.size, start + partSize));
      try {
        const res = await withRetry(
          () =>
            xhrPut(part.url, chunk, {
              headers: part.headers,
              signal: ctl.signal,
              timeoutMs: opts.timeoutMs,
              createXhr: opts.createXhr,
              partNumber: part.part_number,
              onProgress: (loaded) => {
                loadedByPart.set(part.part_number, loaded);
                report(part.part_number);
              },
            }),
          retryOpts({ ...opts, signal: ctl.signal }, () => {
            loadedByPart.set(part.part_number, 0);
            report(part.part_number);
          }),
        );
        if (!res.etag) {
          throw new KadrUploadError(
            `part ${part.part_number}: no ETag in the response; expose the ETag header in the bucket CORS configuration`,
            { partNumber: part.part_number },
          );
        }
        results.push({ part_number: part.part_number, etag: res.etag });
      } catch (err) {
        firstError ??= err;
        ctl.abort();
        return;
      }
    }
  };

  try {
    await Promise.all(Array.from({ length: Math.min(concurrency, ordered.length) }, worker));
  } finally {
    opts.signal?.removeEventListener("abort", onOuterAbort);
  }
  if (firstError !== undefined) throw firstError as Error;
  throwIfAborted(opts.signal);
  return results.sort((a, b) => a.part_number - b.part_number);
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
