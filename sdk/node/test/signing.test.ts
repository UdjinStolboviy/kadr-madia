import { describe, expect, it } from "vitest";
import { Kadr, canonicalString, signUrl, verifySignedUrl, type SigningConfig } from "../src/index.js";

const signing: SigningConfig = {
  kid: "k3",
  secret: "test-secret-0123456789abcdef",
  baseUrl: "https://media.example.com",
};
const NOW = 1763326800_000; // exp = 1763330400 with ttl 3600
const KEY = "acme/blb_01JDTEST/main.avif";

describe("signUrl", () => {
  it("builds the canonical string", () => {
    expect(canonicalString(KEY, 1763330400, "receipt.avif")).toBe(
      "GET\n/acme/blb_01JDTEST/main.avif\n1763330400\nreceipt.avif",
    );
    expect(canonicalString(KEY, 1763330400)).toBe("GET\n/acme/blb_01JDTEST/main.avif\n1763330400\n");
  });

  it("matches the fixed vector with download", () => {
    expect(signUrl(signing, KEY, { ttl: 3600, download: "receipt.avif", now: NOW })).toBe(
      "https://media.example.com/acme/blb_01JDTEST/main.avif?exp=1763330400&kid=k3" +
        "&sig=6htVHR_ussyeRR5QQBVvsmobaFla_5qrDeHoDtYVw5M&download=receipt.avif",
    );
  });

  it("matches the fixed vector without download", () => {
    expect(signUrl(signing, KEY, { ttl: 3600, now: NOW })).toBe(
      "https://media.example.com/acme/blb_01JDTEST/main.avif?exp=1763330400&kid=k3" +
        "&sig=O15QXzAjaI3KPNfA_FguxK3zQPZD0s-YMgTTH8SDHNU",
    );
  });

  it("strips leading slashes and tolerates a trailing slash in baseUrl", () => {
    const url = signUrl({ ...signing, baseUrl: "https://media.example.com/" }, `/${KEY}`, {
      ttl: 3600,
      now: NOW,
    });
    expect(url).toBe(signUrl(signing, KEY, { ttl: 3600, now: NOW }));
  });

  it("signs the raw key but percent-encodes each path segment", () => {
    const url = signUrl(signing, "acme/dir with space/ä.avif", {
      ttl: 3600,
      download: "my file.avif",
      now: NOW,
    });
    expect(url).toBe(
      "https://media.example.com/acme/dir%20with%20space/%C3%A4.avif?exp=1763330400&kid=k3" +
        "&sig=oLaVkBAHWkacbBH3FG2I9Y9og7e34_jDu9EBOzCpQP4&download=my%20file.avif",
    );
    const res = verifySignedUrl(url, { secrets: { k3: signing.secret }, now: NOW });
    expect(res).toEqual({
      ok: true,
      key: "acme/dir with space/ä.avif",
      exp: 1763330400,
      kid: "k3",
      download: "my file.avif",
    });
  });

  it("rejects invalid ttl and keys", () => {
    expect(() => signUrl(signing, KEY, { ttl: 0 })).toThrow(RangeError);
    expect(() => signUrl(signing, KEY, { ttl: 1.5 })).toThrow(RangeError);
    expect(() => signUrl(signing, "/", { ttl: 60 })).toThrow(TypeError);
    expect(() => signUrl(signing, "a/../b", { ttl: 60 })).toThrow(TypeError);
  });

  it("is exposed on the client", () => {
    const kadr = new Kadr({ baseUrl: "http://kadr.internal:8080", apiKey: "kd_test_x", signing });
    expect(kadr.signUrl(KEY, { ttl: 3600, now: NOW })).toBe(signUrl(signing, KEY, { ttl: 3600, now: NOW }));
    const noSigning = new Kadr({ baseUrl: "http://kadr.internal:8080", apiKey: "kd_test_x" });
    expect(() => noSigning.signUrl(KEY, { ttl: 60 })).toThrow(/signing/);
  });
});

describe("verifySignedUrl", () => {
  const url = signUrl(signing, KEY, { ttl: 3600, download: "receipt.avif", now: NOW });

  it("accepts a valid URL (record and resolver)", () => {
    const expected = { ok: true, key: KEY, exp: 1763330400, kid: "k3", download: "receipt.avif" };
    expect(verifySignedUrl(url, { secrets: { k3: signing.secret }, now: NOW })).toEqual(expected);
    expect(
      verifySignedUrl(new URL(url), {
        secrets: (kid) => (kid === "k3" ? signing.secret : undefined),
        now: NOW + 3600_000, // exactly at exp: still valid
      }),
    ).toEqual(expected);
  });

  it("rejects tampered URLs", () => {
    const secrets = { k3: signing.secret };
    expect(verifySignedUrl(url.replace("main.avif", "other.avif"), { secrets, now: NOW })).toEqual({
      ok: false,
      reason: "bad_signature",
    });
    expect(verifySignedUrl(url.replace("exp=1763330400", "exp=1763330999"), { secrets, now: NOW })).toEqual({
      ok: false,
      reason: "bad_signature",
    });
    expect(verifySignedUrl(url.replace("download=receipt.avif", "download=x.avif"), { secrets, now: NOW })).toEqual({
      ok: false,
      reason: "bad_signature",
    });
    expect(verifySignedUrl(url.replace("&download=receipt.avif", ""), { secrets, now: NOW })).toEqual({
      ok: false,
      reason: "bad_signature",
    });
    expect(verifySignedUrl(url, { secrets: { k3: "wrong" }, now: NOW })).toEqual({
      ok: false,
      reason: "bad_signature",
    });
  });

  it("rejects expired URLs", () => {
    expect(verifySignedUrl(url, { secrets: { k3: signing.secret }, now: NOW + 3601_000 })).toEqual({
      ok: false,
      reason: "expired",
    });
  });

  it("rejects unknown kid, missing params and malformed input", () => {
    expect(verifySignedUrl(url, { secrets: { k4: "x" }, now: NOW })).toEqual({ ok: false, reason: "unknown_kid" });
    expect(verifySignedUrl(url, { secrets: () => undefined, now: NOW })).toEqual({
      ok: false,
      reason: "unknown_kid",
    });
    expect(verifySignedUrl(url.replace(/&sig=[^&]+/, ""), { secrets: { k3: "x" } })).toEqual({
      ok: false,
      reason: "missing_params",
    });
    expect(verifySignedUrl("not a url", { secrets: {} })).toEqual({ ok: false, reason: "malformed" });
  });

  it("supports a delivery path prefix", () => {
    const prefixed = signUrl({ ...signing, baseUrl: "https://example.com/files" }, KEY, { ttl: 60, now: NOW });
    expect(verifySignedUrl(prefixed, { secrets: { k3: signing.secret }, now: NOW, pathPrefix: "/files" })).toMatchObject({
      ok: true,
      key: KEY,
    });
  });
});
