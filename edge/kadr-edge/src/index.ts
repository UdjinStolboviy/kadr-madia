import { parseRange } from "./range.ts";
import { verifySignedRequest } from "./signing.ts";
import type { CacheLike, Deps, Env, ExecutionContextLike, R2ObjectLike } from "./types.ts";

export type { Env } from "./types.ts";

/** Objects up to this size are kept in the Cache API (full bodies only). */
export const CACHE_MAX_BYTES = 5 * 1024 * 1024;
/** Upper bound of browser/edge cache lifetime, further capped by the URL's remaining validity. */
export const MAX_CACHE_SECONDS = 3600;

const BASE_HEADERS: Record<string, string> = {
  "X-Content-Type-Options": "nosniff",
  "Referrer-Policy": "no-referrer",
};

function text(status: number, body: string, extra: Record<string, string> = {}): Response {
  return new Response(body, {
    status,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store", ...BASE_HEADERS, ...extra },
  });
}

/** RFC 6266 / 5987 attachment disposition with ASCII fallback. */
export function contentDisposition(filename: string): string {
  const clean = filename.replace(/[\x00-\x1f\x7f]/g, "").replace(/[\\/]/g, "_");
  const ascii = clean.replace(/[^\x20-\x7e]/g, "_").replace(/["\\%]/g, "_");
  const star = encodeURIComponent(clean).replace(/['()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
  return `attachment; filename="${ascii}"; filename*=UTF-8''${star}`;
}

/** Cache key: origin + path only, i.e. without exp/kid/sig/download. */
export function cacheKeyFor(url: URL): Request {
  return new Request(`${url.origin}${url.pathname}`, { method: "GET" });
}

function etagMatches(header: string | null, etag: string): boolean {
  if (!header) return false;
  if (header.trim() === "*") return true;
  const strip = (s: string) => s.trim().replace(/^W\//, "");
  const want = strip(etag);
  return header.split(",").some((t) => strip(t) === want);
}

interface Meta {
  size: number;
  etag: string;
  contentType: string;
  lastModified: string;
}

function metaOf(o: R2ObjectLike): Meta {
  return {
    size: o.size,
    etag: o.httpEtag,
    contentType: o.httpMetadata?.contentType || "application/octet-stream",
    lastModified: o.uploaded.toUTCString(),
  };
}

function baseHeaders(meta: Meta, exp: number, nowMs: number, download?: string): Headers {
  const ttl = Math.max(0, Math.min(MAX_CACHE_SECONDS, exp - Math.floor(nowMs / 1000)));
  const h = new Headers(BASE_HEADERS);
  h.set("Content-Type", meta.contentType);
  h.set("ETag", meta.etag);
  h.set("Last-Modified", meta.lastModified);
  h.set("Accept-Ranges", "bytes");
  h.set("Cache-Control", `private, max-age=${ttl}`);
  if (download !== undefined) h.set("Content-Disposition", contentDisposition(download));
  return h;
}

export async function handle(
  request: Request,
  env: Env,
  ctx: ExecutionContextLike,
  deps: Deps = {},
): Promise<Response> {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return text(405, "method not allowed", { Allow: "GET, HEAD" });
  }
  const nowMs = deps.now ?? Date.now();
  const url = new URL(request.url);
  const v = await verifySignedRequest(url, env, nowMs);
  if (!v.ok) return text(403, "forbidden"); // reason deliberately not disclosed
  const { key, exp, download } = v;
  const isHead = request.method === "HEAD";
  const cache: CacheLike | undefined = deps.cache;
  const cacheKey = cacheKeyFor(url);
  const inm = request.headers.get("If-None-Match");
  const rangeHeader = request.headers.get("Range");

  // Serve from a fully-buffered body (cache hit or small fresh object).
  const fromBuffer = (meta: Meta, bytes: ArrayBuffer): Response => {
    const h = baseHeaders(meta, exp, nowMs, download);
    if (etagMatches(inm, meta.etag)) return new Response(null, { status: 304, headers: h });
    return respondFromBytes(request, h, meta, bytes, isHead);
  };

  if (cache && !isHead) {
    const hit = await cache.match(cacheKey);
    if (hit) {
      const meta: Meta = {
        size: Number(hit.headers.get("Content-Length") ?? 0),
        etag: hit.headers.get("ETag") ?? "",
        contentType: hit.headers.get("Content-Type") ?? "application/octet-stream",
        lastModified: hit.headers.get("Last-Modified") ?? "",
      };
      return fromBuffer(meta, await hit.arrayBuffer());
    }
  }

  // Metadata first when the answer may not need a body (HEAD, conditional, range).
  if (isHead || inm || rangeHeader) {
    const head = await env.PRIVATE.head(key);
    if (!head) return text(404, "not found");
    const meta = metaOf(head);
    const h = baseHeaders(meta, exp, nowMs, download);
    if (etagMatches(inm, meta.etag)) return new Response(null, { status: 304, headers: h });

    const useRange = rangeHeader && !ifRangeRejects(request.headers.get("If-Range"), meta);
    const r = useRange ? parseRange(rangeHeader, meta.size) : ({ kind: "none" } as const);
    if (r.kind === "unsatisfiable") {
      h.set("Content-Range", `bytes */${meta.size}`);
      return new Response(null, { status: 416, headers: h });
    }
    if (r.kind === "range") {
      const len = r.end - r.start + 1;
      h.set("Content-Range", `bytes ${r.start}-${r.end}/${meta.size}`);
      h.set("Content-Length", String(len));
      if (isHead) return new Response(null, { status: 206, headers: h });
      const obj = await env.PRIVATE.get(key, { range: { offset: r.start, length: len } });
      if (!obj) return text(404, "not found");
      return new Response(obj.body, { status: 206, headers: h });
    }
    h.set("Content-Length", String(meta.size));
    if (isHead) return new Response(null, { status: 200, headers: h });
    // Fall through to a full GET below (conditional did not match / no range).
    return fullGet(env, key, h, meta, cache, cacheKey, ctx);
  }

  const obj = await env.PRIVATE.get(key);
  if (!obj) return text(404, "not found");
  const meta = metaOf(obj);
  const h = baseHeaders(meta, exp, nowMs, download);
  h.set("Content-Length", String(meta.size));
  return streamOrCache(obj.body, h, meta, cache, cacheKey, ctx);
}

function ifRangeRejects(ifRange: string | null, meta: Meta): boolean {
  if (!ifRange) return false;
  return ifRange.trim() !== meta.etag && ifRange.trim() !== meta.lastModified;
}

async function fullGet(
  env: Env,
  key: string,
  h: Headers,
  meta: Meta,
  cache: CacheLike | undefined,
  cacheKey: Request,
  ctx: ExecutionContextLike,
): Promise<Response> {
  const obj = await env.PRIVATE.get(key);
  if (!obj) return text(404, "not found");
  return streamOrCache(obj.body, h, meta, cache, cacheKey, ctx);
}

async function streamOrCache(
  body: ReadableStream<Uint8Array>,
  h: Headers,
  meta: Meta,
  cache: CacheLike | undefined,
  cacheKey: Request,
  ctx: ExecutionContextLike,
): Promise<Response> {
  if (!cache || meta.size > CACHE_MAX_BYTES) return new Response(body, { status: 200, headers: h });
  const bytes = await new Response(body).arrayBuffer();
  // Stored copy is edge-internal and keyed without the signature. It carries no
  // Content-Disposition; the per-request value is added on the way out.
  const stored = new Headers({
    "Content-Type": meta.contentType,
    "Content-Length": String(bytes.byteLength),
    ETag: meta.etag,
    "Last-Modified": meta.lastModified,
    "Cache-Control": `public, max-age=${MAX_CACHE_SECONDS}`,
  });
  ctx.waitUntil(cache.put(cacheKey, new Response(bytes.slice(0), { status: 200, headers: stored })));
  return new Response(bytes, { status: 200, headers: h });
}

function respondFromBytes(request: Request, h: Headers, meta: Meta, bytes: ArrayBuffer, isHead: boolean): Response {
  const r = parseRange(request.headers.get("Range"), meta.size);
  const rangeOk = !ifRangeRejects(request.headers.get("If-Range"), meta);
  const range = rangeOk ? r : ({ kind: "none" } as const);
  if (range.kind === "unsatisfiable") {
    h.set("Content-Range", `bytes */${meta.size}`);
    return new Response(null, { status: 416, headers: h });
  }
  if (range.kind === "range") {
    h.set("Content-Range", `bytes ${range.start}-${range.end}/${meta.size}`);
    h.set("Content-Length", String(range.end - range.start + 1));
    return new Response(isHead ? null : bytes.slice(range.start, range.end + 1), { status: 206, headers: h });
  }
  h.set("Content-Length", String(bytes.byteLength));
  return new Response(isHead ? null : bytes, { status: 200, headers: h });
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContextLike): Promise<Response> {
    const cache = (globalThis as unknown as { caches?: { default?: CacheLike } }).caches?.default;
    return handle(request, env, ctx, { cache });
  },
};
