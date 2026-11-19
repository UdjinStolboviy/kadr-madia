import { createHash, createHmac } from "node:crypto";

/**
 * Direct-read URLs for private files: an S3 SigV4 presigned GET on the bucket
 * itself (Cloudflare R2 or any S3-compatible provider), so browsers download
 * straight from storage — no Worker, no delivery host, no request to Kadr.
 *
 * Use credentials that can only READ the private bucket (an R2 API token with
 * "Object Read only" on that bucket): rotating that token revokes every URL
 * it signed. Synchronous and dependency-free, so it can run inside response
 * interceptors. Matches Kadr's own presigning (POST /v1/signed-urls without a
 * delivery host) byte for byte.
 */
export interface PresignConfig {
  /** S3 endpoint, e.g. `https://<account>.r2.cloudflarestorage.com`. */
  endpoint: string;
  /** Private bucket name. */
  bucket: string;
  accessKeyId: string;
  secretAccessKey: string;
  /** Signing region; `auto` for R2. */
  region?: string;
}

export interface PresignGetOptions {
  /** Minimal remaining validity in seconds (default 3600). */
  ttl?: number;
  /** Serve as an attachment with this file name (response-content-disposition). */
  download?: string;
  /**
   * Window in seconds within which an object keeps the same URL, so browser
   * and CDN caches hit (default `min(ttl, 3600)`; 0 disables). The URL is then
   * valid for `ttl + window` from the window start, i.e. at least `ttl`.
   */
  stableWindow?: number;
  /** Current time in milliseconds (tests). */
  now?: number;
}

/** SigV4 caps a presigned URL at 7 days. */
export const MAX_PRESIGN_SECONDS = 7 * 24 * 3600;

/** RFC 3986 encoding as S3 expects it (unreserved characters only). */
function encode(s: string): string {
  return encodeURIComponent(s).replace(
    /[!'()*]/g,
    (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`,
  );
}

/** RFC 6266 / 5987 attachment disposition with an ASCII fallback (same as Kadr). */
export function attachmentDisposition(filename: string): string {
  let clean = "";
  for (const c of filename) {
    const code = c.charCodeAt(0);
    if (code >= 0x20 && code !== 0x7f)
      clean += c === "/" || c === "\\" ? "_" : c;
  }
  const ascii = clean.replace(/[^\x20-\x7e]/g, "_").replace(/["\\%]/g, "_");
  return `attachment; filename="${ascii}"; filename*=UTF-8''${encode(clean)}`;
}

function amzDate(d: Date): string {
  return d
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d{3}/, "");
}

/** Returns a presigned GET URL for `key` in the configured bucket. */
export function presignGet(
  config: PresignConfig,
  key: string,
  options: PresignGetOptions = {},
): string {
  const ttl = options.ttl ?? 3600;
  if (!Number.isInteger(ttl) || ttl <= 0)
    throw new RangeError("ttl must be a positive integer");
  const nowSec = Math.floor((options.now ?? Date.now()) / 1000);
  const window = options.stableWindow ?? Math.min(ttl, 3600);
  const signAt = window > 0 ? nowSec - (nowSec % window) : nowSec;
  const expires = Math.min(
    ttl + (window > 0 ? window : 0),
    MAX_PRESIGN_SECONDS,
  );

  const endpoint = new URL(config.endpoint);
  const region = config.region || "auto";
  const date = amzDate(new Date(signAt * 1000));
  const scope = `${date.slice(0, 8)}/${region}/s3/aws4_request`;
  const path =
    `${endpoint.pathname.replace(/\/+$/, "")}/${encode(config.bucket)}/` +
    key.replace(/^\/+/, "").split("/").map(encode).join("/");

  const params: Record<string, string> = {
    "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
    "X-Amz-Credential": `${config.accessKeyId}/${scope}`,
    "X-Amz-Date": date,
    "X-Amz-Expires": String(expires),
    "X-Amz-SignedHeaders": "host",
    "x-id": "GetObject",
  };
  if (options.download)
    params["response-content-disposition"] = attachmentDisposition(
      options.download,
    );
  const query = Object.entries(params)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([k, v]) => `${encode(k)}=${encode(v)}`)
    .join("&");

  const canonicalRequest = [
    "GET",
    path,
    query,
    `host:${endpoint.host}`,
    "",
    "host",
    "UNSIGNED-PAYLOAD",
  ].join("\n");
  const stringToSign = [
    "AWS4-HMAC-SHA256",
    date,
    scope,
    createHash("sha256").update(canonicalRequest).digest("hex"),
  ].join("\n");
  let k: Buffer = createHmac("sha256", `AWS4${config.secretAccessKey}`)
    .update(date.slice(0, 8))
    .digest();
  for (const part of [region, "s3", "aws4_request"])
    k = createHmac("sha256", k).update(part).digest();
  const signature = createHmac("sha256", k).update(stringToSign).digest("hex");
  return `${endpoint.origin}${path}?${query}&X-Amz-Signature=${signature}`;
}
