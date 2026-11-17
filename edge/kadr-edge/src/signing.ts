import type { Env } from "./types.ts";

/**
 * Canonical string; MUST stay identical to sdk/node/src/signing.ts:
 *   GET\n/{key}\n{exp}\n{download}
 * `key` is the decoded storage key without leading slashes; `download` is the
 * decoded filename or "".
 */
export function canonicalString(key: string, exp: number, download?: string): string {
  return `GET\n/${key}\n${exp}\n${download ?? ""}`;
}

/**
 * Canonical string of the directory-scoped mode (`prefix=1`, HLS packages):
 *   GETP\n/{dir}/\n{exp}\n
 * `dir` is the decoded directory of the requested key with a trailing slash.
 * The signature authorizes every object directly in that directory; the
 * distinct "GETP" verb keeps it from ever matching a per-object signature.
 */
export function canonicalPrefixString(dir: string, exp: number): string {
  return `GETP\n/${dir}\n${exp}\n`;
}

export type VerifyResult =
  | { ok: true; key: string; exp: number; kid: string; download?: string; prefix?: boolean }
  | { ok: false; reason: string };

const enc = new TextEncoder();
const B64URL = /^[A-Za-z0-9_-]+$/;

function b64urlDecode(s: string): Uint8Array | null {
  if (!B64URL.test(s)) return null;
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  try {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

/** Looks up the secret for a kid: KV entry first, then the SIGNING_KEYS JSON secret. */
export async function resolveSecret(env: Env, kid: string): Promise<string | undefined> {
  if (env.KEYS) {
    const v = await env.KEYS.get(kid);
    if (v) return v;
  }
  if (env.SIGNING_KEYS) {
    try {
      const map: unknown = JSON.parse(env.SIGNING_KEYS);
      if (map && typeof map === "object" && Object.prototype.hasOwnProperty.call(map, kid)) {
        const v = (map as Record<string, unknown>)[kid];
        if (typeof v === "string" && v) return v;
      }
    } catch {
      // Misconfigured secret: treat as no key (fail closed).
    }
  }
  return undefined;
}

/** Extracts the storage key from a pathname (optionally stripping a prefix). */
export function keyFromPath(pathname: string, prefix?: string): string | null {
  let path = pathname;
  if (prefix) {
    const p = `/${prefix.replace(/^\/+|\/+$/g, "")}`;
    if (p !== "/") {
      if (!path.startsWith(`${p}/`)) return null;
      path = path.slice(p.length);
    }
  }
  let key: string;
  try {
    key = path.split("/").map(decodeURIComponent).join("/").replace(/^\/+/, "");
  } catch {
    return null;
  }
  if (!key || key.includes("\0")) return null;
  if (key.split("/").some((s) => s === "." || s === "..")) return null;
  return key;
}

/**
 * Verifies exp/kid/sig(/download). The signature check uses WebCrypto
 * `subtle.verify`, which compares MACs in constant time. Expiry is checked
 * only after the signature.
 */
export async function verifySignedRequest(url: URL, env: Env, nowMs: number): Promise<VerifyResult> {
  const p = url.searchParams;
  const expRaw = p.get("exp");
  const kid = p.get("kid");
  const sig = p.get("sig");
  if (expRaw === null || kid === null || sig === null) return { ok: false, reason: "missing_params" };
  if (!/^\d{1,15}$/.test(expRaw)) return { ok: false, reason: "malformed" };
  const exp = Number(expRaw);
  const prefix = p.get("prefix") === "1";
  const dl = p.get("download");
  // Directory-scoped signatures do not cover a download name: it is ignored.
  const download = prefix || dl === null || dl === "" ? undefined : dl;

  const key = keyFromPath(url.pathname, env.PATH_PREFIX);
  if (key === null) return { ok: false, reason: "malformed" };

  const sigBytes = b64urlDecode(sig);
  if (!sigBytes || sigBytes.length !== 32) return { ok: false, reason: "bad_signature" };

  const secret = await resolveSecret(env, kid);
  if (!secret) return { ok: false, reason: "unknown_kid" };

  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"],
  );
  const valid = await crypto.subtle.verify(
    "HMAC",
    cryptoKey,
    sigBytes,
    enc.encode(prefix ? canonicalPrefixString(key.slice(0, key.lastIndexOf("/") + 1), exp) : canonicalString(key, exp, download)),
  );
  if (!valid) return { ok: false, reason: "bad_signature" };
  if (Math.floor(nowMs / 1000) > exp) return { ok: false, reason: "expired" };

  const base = { ok: true as const, key, exp, kid };
  if (prefix) return { ...base, prefix: true };
  return download === undefined ? base : { ...base, download };
}
