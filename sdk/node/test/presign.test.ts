import { describe, expect, it } from "vitest";
import { attachmentDisposition, presignGet } from "../src/index.js";

const config = {
  endpoint: "https://acc.r2.cloudflarestorage.com",
  bucket: "priv",
  accessKeyId: "AKIDEXAMPLE",
  secretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
};
const key = "acme/image/2026/11/blb_1/main 1080x1350(ф).avif";
const now = Date.UTC(2026, 10, 19, 10, 5, 0);

describe("presignGet", () => {
  it("matches Kadr's Go presigner byte for byte", () => {
    // Produced by storage.S3.PresignDownload with the same inputs.
    expect(
      presignGet(config, key, { ttl: 3600, download: "ф.avif", now }),
    ).toBe(
      "https://acc.r2.cloudflarestorage.com/priv/acme/image/2026/11/blb_1/main%201080x1350%28%D1%84%29.avif" +
        "?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20261119%2Fauto%2Fs3%2Faws4_request" +
        "&X-Amz-Date=20261119T100000Z&X-Amz-Expires=7200&X-Amz-SignedHeaders=host" +
        "&response-content-disposition=attachment%3B%20filename%3D%22_.avif%22%3B%20filename%2A%3DUTF-8%27%27%25D1%2584.avif" +
        "&x-id=GetObject&X-Amz-Signature=c143c1511f790be9f9eaa4e7f49f46ff6324e7967c5f6164b88b9902883ff464",
    );
  });

  it("keeps one URL per window and caps at 7 days", () => {
    const a = presignGet(config, "k", { now });
    expect(presignGet(config, "k", { now: now + 50 * 60_000 })).toBe(a);
    expect(presignGet(config, "k", { now: now + 60 * 60_000 })).not.toBe(a);
    expect(presignGet(config, "k", { ttl: 30 * 86400, now })).toContain(
      "X-Amz-Expires=604800",
    );
    expect(() => presignGet(config, "k", { ttl: 0 })).toThrow(RangeError);
  });

  it("builds RFC 6266 dispositions", () => {
    expect(attachmentDisposition('звіт "1".pdf')).toBe(
      `attachment; filename="____ _1_.pdf"; filename*=UTF-8''%D0%B7%D0%B2%D1%96%D1%82%20%221%22.pdf`,
    );
  });
});
