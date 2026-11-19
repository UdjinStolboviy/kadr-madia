import { describe, expect, it } from "vitest";

import { summarizePreset } from "./presets";

describe("summarizePreset", () => {
  it("lists exactly the stored image formats", () => {
    const one = summarizePreset("image", { kind: "image", outputs: [{ name: "main", format: "avif" }] });
    expect(one.headline.startsWith("AVIF ·") || one.headline === "AVIF").toBe(true);
    const two = summarizePreset("image", { kind: "image", outputs: [{ format: "avif" }, { format: "webp" }] });
    expect(two.headline).toContain("AVIF + WebP");
  });

  it("shows poster and document thumbnail formats (empty = AVIF + WebP on the server)", () => {
    const video = summarizePreset("video", { kind: "video", video: { codec: "av1", crf: 36 }, poster: { maxBytes: 15360, formats: ["avif"] } });
    expect(video.headline).toContain("AV1");
    expect(video.headline).toMatch(/AVIF$/);
    expect(video.headline).not.toContain("WebP");
    const file = summarizePreset("file", { kind: "file", thumbnail: { enabled: true, maxBytes: 20480 } });
    expect(file.headline).toContain("AVIF + WebP");
  });
});
