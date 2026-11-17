import { inflateSync } from "node:zlib";
import { describe, expect, it } from "vitest";
import {
  base64ToBytes,
  safeThumbHashToDataURL,
  thumbHashToApproximateAspectRatio,
  thumbHashToDataURL,
  thumbHashToRGBA,
} from "../src/index.js";

// Produced by the Go encoder (internal/engine/image/thumbhash.go) for a 16x8 image:
// R = x*16 (horizontal ramp), G = y*30, B = 200. Go-side average colour: 0.468 0.421 0.778.
const HASH = "YxUKNJpwd3dxiHh3iHdwcQjnhw==";

describe("thumbhash", () => {
  it("decodes the aspect ratio and a plausible image", () => {
    const bytes = base64ToBytes(HASH);
    expect(thumbHashToApproximateAspectRatio(bytes)).toBe(1.75);
    const { width, height, rgba } = thumbHashToRGBA(bytes);
    expect([width, height]).toEqual([32, 18]);
    expect(rgba).toHaveLength(32 * 18 * 4);

    let r = 0, g = 0, b = 0;
    for (let i = 0; i < rgba.length; i += 4) {
      r += rgba[i]!; g += rgba[i + 1]!; b += rgba[i + 2]!;
    }
    const n = width * height;
    expect(r / n / 255).toBeCloseTo(0.468, 1);
    expect(g / n / 255).toBeCloseTo(0.421, 1);
    expect(b / n / 255).toBeCloseTo(0.778, 1);
    // horizontal red ramp survives: right side redder than left
    const px = (x: number, y: number) => rgba[(y * width + x) * 4]!;
    expect(px(30, 9)).toBeGreaterThan(px(1, 9) + 40);
  });

  it("accepts url-safe, unpadded base64", () => {
    const alt = HASH.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    expect(base64ToBytes(alt)).toEqual(base64ToBytes(HASH));
  });

  it("emits a valid PNG data URL", () => {
    const url = thumbHashToDataURL(HASH);
    expect(url.startsWith("data:image/png;base64,")).toBe(true);
    const png = Buffer.from(url.split(",")[1]!, "base64");
    expect(png.subarray(0, 8)).toEqual(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));
    expect(png.readUInt32BE(16)).toBe(32);
    expect(png.readUInt32BE(20)).toBe(18);
    const idatLen = png.readUInt32BE(33);
    expect(png.subarray(37, 41).toString()).toBe("IDAT");
    const raw = inflateSync(png.subarray(41, 41 + idatLen)); // also verifies the Adler-32
    expect(raw).toHaveLength(18 * (32 * 4 + 1));
    expect(png.subarray(png.length - 8, png.length - 4).toString()).toBe("IEND");
  });

  it("is forgiving about bad input", () => {
    expect(safeThumbHashToDataURL(undefined)).toBeUndefined();
    expect(safeThumbHashToDataURL("AAA")).toBeUndefined();
    expect(safeThumbHashToDataURL("!!!notbase64")).toBeUndefined();
  });
});
