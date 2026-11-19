import { createHmac, timingSafeEqual } from "node:crypto";

/**
 * Signed URLs for private files, verified by the edge Worker.
 *
 * Canonical string (UTF-8, fields joined by a single LF, no trailing LF):
 *
 *     GET\n/{key}\n{exp}\n{download}
 *
 * - `key`: the storage key with leading slashes stripped, raw (NOT
 *   percent-encoded). The canonical path is `/` + key.
 * - `exp`: Unix time in seconds, decimal integer.
 * - `download`: the raw (decoded) download filename, or the empty string.
 *
 * `sig` = base64url without padding of HMAC-SHA256(key = UTF-8 bytes of the
 * secret string, message = canonical string).
 */

export interface SigningConfig {
  /** Key id of the active signing key. Sent as the `kid` query parameter. */
  kid: string;
  /** Signing secret. Used as the raw UTF-8 bytes of this string (it is NOT base64-decoded). */
  secret: string;
  /** Origin of the private delivery host, e.g. `https://media.example.com`. */
  baseUrl: string;
}

export interface SignUrlOptions {
  /** Lifetime in seconds (positive integer). */
  ttl: number;
  /** Ask the edge to serve the file as an attachment with this filename. Ignored when `prefix: true`. */
  download?: string;
  /**
   * Directory-scoped signature (`prefix=1`, for HLS packages): authorizes any
   * object directly inside the key's directory. `download` is ignored.
   */
  prefix?: boolean;
  /** Current time in milliseconds (for tests). Defaults to `Date.now()`. */
  now?: number;
}

export type SecretResolver =
  | Record<string, string>
  | ((kid: string) => string | undefined | null);

export interface VerifySignedUrlOptions {
  /** Map of `kid` → secret, or a resolver function. */
  secrets: SecretResolver;
  /** Current time in milliseconds (for tests). Defaults to `Date.now()`. */
  now?: number;
  /**
   * Path prefix of the delivery host that is not part of the key
   * (e.g. `/files` when `signing.baseUrl` is `https://example.com/files`).
   */
  pathPrefix?: string;
}

export type VerifySignedUrlFailure =
  | "malformed"
  | "missing_params"
  | "unknown_kid"
  | "bad_signature"
  | "expired";

export type VerifySignedUrlResult =
  | { ok: true; key: string; exp: number; kid: string; download?: string; prefix?: boolean }
  | { ok: false; reason: VerifySignedUrlFailure };

/** Strips leading slashes from a storage key. */
export function normalizeKey(key: string): string {
  return key.replace(/^\/+/, "");
}

/** Builds the canonical string to sign. `key` must already be normalized. */
export function canonicalString(key: string, exp: number, download?: string): string {
  return `GET\n/${key}\n${exp}\n${download ?? ""}`;
}

/**
 * Builds the canonical string of a directory-scoped signature (`prefix=1`, HLS packages):
 * `GETP\n/{dir}\n{exp}\n`. `dir` is the directory prefix with a trailing slash (or `""`).
 */
export function canonicalPrefixString(dir: string, exp: number): string {
  return `GETP\n/${dir}\n${exp}\n`;
}

function hmacBase64Url(secret: string, message: string): string {
  return createHmac("sha256", Buffer.from(secret, "utf8"))
    .update(message, "utf8")
    .digest("base64url");
}

function encodeKeyPath(key: string): string {
  return key.split("/").map(encodeURIComponent).join("/");
}

/**
 * Signs a private storage key and returns the full delivery URL:
 * `${baseUrl}/${key}?exp=…&kid=…&sig=…[&download=…]` (or `&prefix=1` when `opts.prefix` is true).
 */
export function signUrl(config: SigningConfig, key: string, opts: SignUrlOptions): string {
  if (!Number.isInteger(opts.ttl) || opts.ttl <= 0) {
    throw new RangeError("signUrl: ttl must be a positive integer number of seconds");
  }
  const normalized = normalizeKey(key);
  if (!normalized) throw new TypeError("signUrl: key must not be empty");
  if (normalized.split("/").some((s) => s === "." || s === "..")) {
    throw new TypeError('signUrl: key must not contain "." or ".." path segments');
  }
  const now = opts.now ?? Date.now();
  const exp = Math.floor(now / 1000) + opts.ttl;
  const base = config.baseUrl.replace(/\/+$/, "");

  if (opts.prefix) {
    const dir = normalized.slice(0, normalized.lastIndexOf("/") + 1);
    const sig = hmacBase64Url(config.secret, canonicalPrefixString(dir, exp));
    return (
      `${base}/${encodeKeyPath(normalized)}` +
      `?exp=${exp}&kid=${encodeURIComponent(config.kid)}&sig=${sig}&prefix=1`
    );
  }

  const download = opts.download === "" ? undefined : opts.download;
  const sig = hmacBase64Url(config.secret, canonicalString(normalized, exp, download));

  let url =
    `${base}/${encodeKeyPath(normalized)}` +
    `?exp=${exp}&kid=${encodeURIComponent(config.kid)}&sig=${sig}`;
  if (download !== undefined) url += `&download=${encodeURIComponent(download)}`;
  return url;
}

function resolveSecret(secrets: SecretResolver, kid: string): string | undefined {
  if (typeof secrets === "function") return secrets(kid) ?? undefined;
  return Object.prototype.hasOwnProperty.call(secrets, kid) ? secrets[kid] : undefined;
}

function safeEqual(a: string, b: string): boolean {
  const ab = Buffer.from(a, "utf8");
  const bb = Buffer.from(b, "utf8");
  if (ab.length !== bb.length) return false;
  return timingSafeEqual(ab, bb);
}

/**
 * Verifies a signed delivery URL (reference implementation of the edge check).
 * The signature is compared in constant time before the expiry is checked.
 */
export function verifySignedUrl(
  url: string | URL,
  opts: VerifySignedUrlOptions,
): VerifySignedUrlResult {
  let parsed: URL;
  try {
    parsed = typeof url === "string" ? new URL(url) : url;
  } catch {
    return { ok: false, reason: "malformed" };
  }

  const params = parsed.searchParams;
  const expRaw = params.get("exp");
  const kid = params.get("kid");
  const sig = params.get("sig");
  if (expRaw === null || kid === null || sig === null) return { ok: false, reason: "missing_params" };
  if (!/^\d+$/.test(expRaw)) return { ok: false, reason: "malformed" };
  const exp = Number(expRaw);
  const prefix = params.get("prefix") === "1";
  const downloadRaw = params.get("download");
  const download = prefix || downloadRaw === null || downloadRaw === "" ? undefined : downloadRaw;

  let path = parsed.pathname;
  if (opts.pathPrefix) {
    const pfx = `/${opts.pathPrefix.replace(/^\/+|\/+$/g, "")}`;
    if (pfx !== "/") {
      if (!path.startsWith(`${pfx}/`)) return { ok: false, reason: "malformed" };
      path = path.slice(pfx.length);
    }
  }
  let key: string;
  try {
    key = normalizeKey(path.split("/").map(decodeURIComponent).join("/"));
  } catch {
    return { ok: false, reason: "malformed" };
  }
  if (!key) return { ok: false, reason: "malformed" };

  const secret = resolveSecret(opts.secrets, kid);
  if (!secret) return { ok: false, reason: "unknown_kid" };

  const msg = prefix
    ? canonicalPrefixString(key.slice(0, key.lastIndexOf("/") + 1), exp)
    : canonicalString(key, exp, download);
  const expected = hmacBase64Url(secret, msg);
  if (!safeEqual(expected, sig)) return { ok: false, reason: "bad_signature" };

  const nowSec = Math.floor((opts.now ?? Date.now()) / 1000);
  // Valid up to and including the second `exp`.
  if (nowSec > exp) return { ok: false, reason: "expired" };

  if (prefix) return { ok: true, key, exp, kid, prefix: true };
  return download === undefined ? { ok: true, key, exp, kid } : { ok: true, key, exp, kid, download };
}
