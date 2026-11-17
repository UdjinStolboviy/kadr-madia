import { describe, expect, it } from "vitest";
import { Kadr, WebhookVerificationError, signWebhook, verifyWebhook } from "../src/index.js";

// Official Standard Webhooks test vector.
const SECRET = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";
const MSG_ID = "msg_p5jXN8AQM9LWM0D4loKWxJek";
const TS = 1614265330;
const PAYLOAD = '{"test": 2432232314}';
const EXPECTED = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=";
const NOW = TS * 1000;

const headers = (signature: string, ts = TS) => ({
  "webhook-id": MSG_ID,
  "webhook-timestamp": String(ts),
  "webhook-signature": signature,
});

describe("Standard Webhooks vector", () => {
  it("signWebhook produces the official signature", () => {
    expect(signWebhook(SECRET, { id: MSG_ID, timestamp: TS, body: PAYLOAD })).toBe(EXPECTED);
  });

  it("verifyWebhook accepts the official vector (string, Buffer, Uint8Array, Headers)", () => {
    for (const body of [PAYLOAD, Buffer.from(PAYLOAD), new TextEncoder().encode(PAYLOAD)]) {
      const event = verifyWebhook(body, headers(EXPECTED), SECRET, { now: NOW });
      expect(event).toEqual({ id: MSG_ID, test: 2432232314 });
    }
    const h = new Headers(headers(EXPECTED));
    expect(verifyWebhook(PAYLOAD, h, SECRET, { now: NOW })).toMatchObject({ id: MSG_ID });
  });
});

describe("verifyWebhook", () => {
  it("accepts any matching entry among several signatures", () => {
    const sig = `v1,bm90LWEtcmVhbC1zaWduYXR1cmU= v2,ignored ${EXPECTED} v1,AAAA`;
    expect(() => verifyWebhook(PAYLOAD, headers(sig), SECRET, { now: NOW })).not.toThrow();
  });

  it("rejects a bad signature", () => {
    const bad = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OF=";
    expect(() => verifyWebhook(PAYLOAD, headers(bad), SECRET, { now: NOW })).toThrow(WebhookVerificationError);
    expect(() => verifyWebhook(PAYLOAD, headers("v1,short"), SECRET, { now: NOW })).toThrow(
      WebhookVerificationError,
    );
    expect(() => verifyWebhook(`${PAYLOAD} `, headers(EXPECTED), SECRET, { now: NOW })).toThrow(
      WebhookVerificationError,
    );
    expect(() =>
      verifyWebhook(PAYLOAD, headers(EXPECTED), "whsec_" + Buffer.from("other").toString("base64"), { now: NOW }),
    ).toThrow(WebhookVerificationError);
  });

  it("rejects stale and future timestamps outside 5 minutes", () => {
    expect(() => verifyWebhook(PAYLOAD, headers(EXPECTED), SECRET, { now: NOW + 301_000 })).toThrow(/too old/);
    expect(() => verifyWebhook(PAYLOAD, headers(EXPECTED), SECRET, { now: NOW - 301_000 })).toThrow(/too new/);
    // Within tolerance on both sides.
    expect(() => verifyWebhook(PAYLOAD, headers(EXPECTED), SECRET, { now: NOW + 299_000 })).not.toThrow();
    expect(() => verifyWebhook(PAYLOAD, headers(EXPECTED), SECRET, { now: NOW - 299_000 })).not.toThrow();
  });

  it("rejects missing headers", () => {
    expect(() => verifyWebhook(PAYLOAD, { "webhook-id": MSG_ID }, SECRET, { now: NOW })).toThrow(/Missing/);
  });

  it("supports raw (unprefixed) secrets and returns a camelCased typed event", () => {
    const secret = "plain-text-secret";
    const body = JSON.stringify({
      type: "asset.failed",
      timestamp: "2026-11-16T20:59:13Z",
      data: {
        asset: { id: "ast_1", preset_version: 3, metadata: { entity_id: "rcp_123" } },
        error: { code: "corrupt" },
        metadata: { entity_id: "rcp_123" },
      },
    });
    const now = Date.now();
    const ts = Math.floor(now / 1000);
    const sig = signWebhook(secret, { id: "msg_1", timestamp: ts, body });
    const kadr = new Kadr({ baseUrl: "http://kadr.internal:8080", apiKey: "kd_test_x" });
    const event = kadr.webhooks.verify(body, { "Webhook-Id": "msg_1", "webhook-timestamp": [String(ts)], "webhook-signature": sig }, secret);
    expect(event.type).toBe("asset.failed");
    if (event.type === "asset.failed") {
      expect(event.data.error.code).toBe("corrupt");
      expect(event.data.asset.presetVersion).toBe(3);
      expect(event.data.asset.metadata).toEqual({ entity_id: "rcp_123" });
      expect(event.data.metadata).toEqual({ entity_id: "rcp_123" });
    }
    expect(event.id).toBe("msg_1");
  });

  it("maps upload.expired and quota payloads to their camelCased types", () => {
    const secret = "whsec_" + Buffer.from("k".repeat(32)).toString("base64");
    const ts = Math.floor(Date.now() / 1000);
    const verify = (payload: unknown) => {
      const body = JSON.stringify(payload);
      const sig = signWebhook(secret, { id: MSG_ID, timestamp: ts, body });
      return verifyWebhook(body, headers(sig, ts), secret);
    };
    const expired = verify({
      type: "upload.expired",
      timestamp: "2026-11-16T21:15:00Z",
      data: {
        upload_id: "upl_1",
        namespace: "org_8f2c",
        preset: "photo-lite",
        visibility: "private",
        filename: null,
        expires_at: "2026-11-16T21:15:00Z",
        metadata: { entity_id: "x" },
      },
    });
    if (expired.type !== "upload.expired") throw new Error("wrong type");
    expect(expired.data.uploadId).toBe("upl_1");
    expect(expired.data.expiresAt).toBe("2026-11-16T21:15:00Z");
    expect(expired.data.metadata).toEqual({ entity_id: "x" });

    const quota = verify({
      type: "quota.warning",
      timestamp: "2026-11-16T21:15:00Z",
      data: { namespace: "org_8f2c", used_bytes: 850, quota_bytes: 1000, ratio: 0.85 },
    });
    if (quota.type !== "quota.warning") throw new Error("wrong type");
    expect(quota.data).toEqual({ namespace: "org_8f2c", usedBytes: 850, quotaBytes: 1000, ratio: 0.85 });
  });
});
