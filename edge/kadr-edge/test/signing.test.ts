import { describe, expect, it } from "vitest";
import { signUrl as sdkSignUrl, canonicalString as sdkCanonical } from "../../../sdk/node/src/signing.ts";
import { canonicalString, verifySignedRequest } from "../src/signing.ts";
import type { Env } from "../src/types.ts";

const secret = "test-secret-0123456789abcdef";
const signing = { kid: "k3", secret, baseUrl: "https://media.example.com" };
const NOW = 1763326800_000;
const KEY = "acme/blb_01JDTEST/main.avif";
const env = (keys: Record<string, string>): Env => ({ PRIVATE: {} as never, SIGNING_KEYS: JSON.stringify(keys) });

describe("cross-check with Node SDK", () => {
  it("canonical strings are identical", () => {
    expect(canonicalString(KEY, 1763330400, "a b.avif")).toBe(sdkCanonical(KEY, 1763330400, "a b.avif"));
    expect(canonicalString(KEY, 1763330400)).toBe(sdkCanonical(KEY, 1763330400));
  });

  it("accepts SDK-signed URLs (fixed vectors and unicode/space keys)", async () => {
    const e = env({ k3: secret });
    const withDl = sdkSignUrl(signing, KEY, { ttl: 3600, download: "receipt.avif", now: NOW });
    expect(withDl).toContain("sig=6htVHR_ussyeRR5QQBVvsmobaFla_5qrDeHoDtYVw5M");
    expect(await verifySignedRequest(new URL(withDl), e, NOW)).toMatchObject({ ok: true, key: KEY, download: "receipt.avif" });
    const noDl = sdkSignUrl(signing, KEY, { ttl: 3600, now: NOW });
    expect(noDl).toContain("sig=O15QXzAjaI3KPNfA_FguxK3zQPZD0s-YMgTTH8SDHNU");
    expect(await verifySignedRequest(new URL(noDl), e, NOW)).toMatchObject({ ok: true, key: KEY });
    const odd = sdkSignUrl(signing, "acme/dir with space/ä.avif", { ttl: 3600, download: "my file.avif", now: NOW });
    expect(await verifySignedRequest(new URL(odd), e, NOW)).toMatchObject({ ok: true, key: "acme/dir with space/ä.avif" });
  });
});

describe("verifySignedRequest", () => {
  const url = sdkSignUrl(signing, KEY, { ttl: 3600, download: "r.avif", now: NOW });
  const e = env({ k3: secret });
  const reason = async (u: string, ee: Env = e, now = NOW) => {
    const r = await verifySignedRequest(new URL(u), ee, now);
    return r.ok ? "ok" : r.reason;
  };

  it("valid up to and including exp, expired after", async () => {
    expect(await reason(url, e, NOW + 3600_000)).toBe("ok");
    expect(await reason(url, e, NOW + 3601_000)).toBe("expired");
  });
  it("rejects tampering", async () => {
    expect(await reason(url.replace("main.avif", "x.avif"))).toBe("bad_signature");
    expect(await reason(url.replace("1763330400", "1763339999"))).toBe("bad_signature");
    expect(await reason(url.replace("download=r.avif", "download=x.avif"))).toBe("bad_signature");
    expect(await reason(url.replace("&download=r.avif", ""))).toBe("bad_signature");
    expect(await reason(url.replace(/sig=[^&]+/, "sig=AAAA"))).toBe("bad_signature");
    expect(await reason(url, env({ k3: "wrong" }))).toBe("bad_signature");
  });
  it("rejects missing params, unknown kid, bad exp", async () => {
    expect(await reason(url.replace(/&sig=[^&]+/, ""))).toBe("missing_params");
    expect(await reason(url, env({ k9: "x" }))).toBe("unknown_kid");
    expect(await reason(url.replace("exp=1763330400", "exp=abc"))).toBe("malformed");
  });
  it("supports multiple kids and KV lookup", async () => {
    const old = sdkSignUrl({ ...signing, kid: "k2", secret: "old" }, KEY, { ttl: 60, now: NOW });
    const both = env({ k3: secret, k2: "old" });
    expect(await reason(old, both)).toBe("ok");
    expect(await reason(url, both)).toBe("ok");
    const kv = { PRIVATE: {} as never, KEYS: { get: async (k: string) => (k === "k2" ? "old" : null) } };
    expect(await reason(old, kv)).toBe("ok");
  });
  it("fails closed on a broken secret and on prototype kids", async () => {
    expect(await reason(url, { PRIVATE: {} as never, SIGNING_KEYS: "{not json" })).toBe("unknown_kid");
    expect(await reason(url.replace("kid=k3", "kid=constructor"), e)).toBe("unknown_kid");
  });
});
