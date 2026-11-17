/**
 * ThumbHash decoder, a TypeScript port of the reference algorithm by Evan
 * Wallace (https://github.com/evanw/thumbhash, MIT). The hashes are produced by
 * the Go encoder in `internal/engine/image/thumbhash.go` and delivered as
 * base64 in `asset.placeholder.thumbhash`.
 */

/** Decodes standard or URL-safe base64, with or without padding. */
export function base64ToBytes(b64: string): Uint8Array {
  const clean = b64.replace(/-/g, "+").replace(/_/g, "/").replace(/=+$/, "");
  const bin = atob(clean);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** Approximate width / height of the original image encoded in the hash. */
export function thumbHashToApproximateAspectRatio(hash: Uint8Array): number {
  const h2 = hash[2] ?? 0;
  const h3 = hash[3] ?? 0;
  const h4 = hash[4] ?? 0;
  const hasAlpha = (h2 & 0x80) !== 0;
  const lMax = hasAlpha ? 5 : 7;
  const lMin = h3 & 7;
  const isLandscape = (h4 & 0x80) !== 0;
  const lx = isLandscape ? lMax : lMin;
  const ly = isLandscape ? lMin : lMax;
  return lx / ly;
}

export interface DecodedThumbHash {
  width: number;
  height: number;
  /** Non-premultiplied RGBA, `width * height * 4` bytes. */
  rgba: Uint8Array;
}

/** Decodes a ThumbHash into a small (at most 32px) RGBA image. */
export function thumbHashToRGBA(hash: Uint8Array): DecodedThumbHash {
  if (hash.length < 5) throw new Error("thumbhash: too short");
  const b = (i: number) => hash[i] ?? 0;
  const { PI, min, max, cos, round } = Math;

  const header24 = b(0) | (b(1) << 8) | (b(2) << 16);
  const header16 = b(3) | (b(4) << 8);
  const lDc = (header24 & 63) / 63;
  const pDc = ((header24 >> 6) & 63) / 31.5 - 1;
  const qDc = ((header24 >> 12) & 63) / 31.5 - 1;
  const lScale = ((header24 >> 18) & 31) / 31;
  const hasAlpha = header24 >> 23 !== 0;
  const pScale = ((header16 >> 3) & 63) / 63;
  const qScale = ((header16 >> 9) & 63) / 63;
  const isLandscape = header16 >> 15 !== 0;
  const lx = max(3, isLandscape ? (hasAlpha ? 5 : 7) : header16 & 7);
  const ly = max(3, isLandscape ? header16 & 7 : hasAlpha ? 5 : 7);
  if (hasAlpha && hash.length < 6) throw new Error("thumbhash: too short");
  const aDc = hasAlpha ? (b(5) & 15) / 15 : 1;
  const aScale = (b(5) >> 4) / 15;

  const acStart = hasAlpha ? 6 : 5;
  let acIndex = 0;
  const decodeChannel = (nx: number, ny: number, scale: number): number[] => {
    const ac: number[] = [];
    for (let cy = 0; cy < ny; cy++) {
      for (let cx = cy ? 0 : 1; cx * ny < nx * (ny - cy); cx++) {
        const byte = b(acStart + (acIndex >> 1));
        ac.push((((byte >> ((acIndex++ & 1) << 2)) & 15) / 7.5 - 1) * scale);
      }
    }
    return ac;
  };
  const lAc = decodeChannel(lx, ly, lScale);
  const pAc = decodeChannel(3, 3, pScale * 1.25);
  const qAc = decodeChannel(3, 3, qScale * 1.25);
  const aAc = hasAlpha ? decodeChannel(5, 5, aScale) : [];

  const ratio = thumbHashToApproximateAspectRatio(hash);
  const w = max(1, round(ratio > 1 ? 32 : 32 * ratio));
  const h = max(1, round(ratio > 1 ? 32 / ratio : 32));
  const rgba = new Uint8Array(w * h * 4);
  const fx: number[] = [];
  const fy: number[] = [];
  const at = (arr: number[], i: number) => arr[i] ?? 0;

  for (let y = 0, i = 0; y < h; y++) {
    for (let x = 0; x < w; x++, i += 4) {
      let l = lDc;
      let p = pDc;
      let q = qDc;
      let a = aDc;

      for (let cx = 0, n = max(lx, hasAlpha ? 5 : 3); cx < n; cx++) fx[cx] = cos((PI / w) * (x + 0.5) * cx);
      for (let cy = 0, n = max(ly, hasAlpha ? 5 : 3); cy < n; cy++) fy[cy] = cos((PI / h) * (y + 0.5) * cy);

      for (let cy = 0, j = 0; cy < ly; cy++) {
        for (let cx = cy ? 0 : 1, fy2 = at(fy, cy) * 2; cx * ly < lx * (ly - cy); cx++, j++) {
          l += at(lAc, j) * at(fx, cx) * fy2;
        }
      }
      for (let cy = 0, j = 0; cy < 3; cy++) {
        for (let cx = cy ? 0 : 1, fy2 = at(fy, cy) * 2; cx < 3 - cy; cx++, j++) {
          const f = at(fx, cx) * fy2;
          p += at(pAc, j) * f;
          q += at(qAc, j) * f;
        }
      }
      if (hasAlpha) {
        for (let cy = 0, j = 0; cy < 5; cy++) {
          for (let cx = cy ? 0 : 1, fy2 = at(fy, cy) * 2; cx < 5 - cy; cx++, j++) {
            a += at(aAc, j) * at(fx, cx) * fy2;
          }
        }
      }

      const bl = l - (2 / 3) * p;
      const r = (3 * l - bl + q) / 2;
      const g = r - q;
      rgba[i] = max(0, 255 * min(1, r));
      rgba[i + 1] = max(0, 255 * min(1, g));
      rgba[i + 2] = max(0, 255 * min(1, bl));
      rgba[i + 3] = max(0, 255 * min(1, a));
    }
  }
  return { width: w, height: h, rgba };
}

let crcTable: Uint32Array | undefined;
function crc32(bytes: Uint8Array, start: number, end: number): number {
  if (!crcTable) {
    crcTable = new Uint32Array(256);
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      crcTable[n] = c >>> 0;
    }
  }
  let c = 0xffffffff;
  for (let i = start; i < end; i++) c = (crcTable[(c ^ (bytes[i] ?? 0)) & 255] ?? 0) ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

/** Encodes RGBA as an uncompressed PNG (tiny images only; no canvas needed, so it works in SSR). */
export function rgbaToPng(width: number, height: number, rgba: Uint8Array): Uint8Array {
  const row = width * 4 + 1;
  // zlib header (2) + one stored deflate block per row (5 + row) + adler32 (4)
  const idatLen = 2 + height * (5 + row) + 4;
  const total = 8 + 25 + (12 + idatLen) + 12;
  const out = new Uint8Array(total);
  const dv = new DataView(out.buffer);
  out.set([137, 80, 78, 71, 13, 10, 26, 10], 0);
  let o = 8;
  const chunk = (type: string, len: number): number => {
    dv.setUint32(o, len);
    for (let i = 0; i < 4; i++) out[o + 4 + i] = type.charCodeAt(i);
    return o + 8;
  };
  const end = (dataStart: number, len: number) => {
    dv.setUint32(dataStart + len, crc32(out, dataStart - 4, dataStart + len));
    o = dataStart + len + 4;
  };

  let p = chunk("IHDR", 13);
  dv.setUint32(p, width);
  dv.setUint32(p + 4, height);
  out[p + 8] = 8; // bit depth
  out[p + 9] = 6; // RGBA
  end(p, 13);

  p = chunk("IDAT", idatLen);
  const start = p;
  out[p++] = 0x78;
  out[p++] = 0x01;
  let a = 1;
  let bsum = 0;
  for (let y = 0; y < height; y++) {
    out[p++] = y === height - 1 ? 1 : 0; // BFINAL on the last block, stored
    out[p++] = row & 255;
    out[p++] = row >> 8;
    out[p++] = ~row & 255;
    out[p++] = (~row >> 8) & 255;
    const rowStart = p;
    out[p++] = 0; // filter: none
    for (let i = 0; i < width * 4; i++) out[p++] = rgba[y * width * 4 + i] ?? 0;
    for (let i = rowStart; i < p; i++) {
      a = (a + (out[i] ?? 0)) % 65521;
      bsum = (bsum + a) % 65521;
    }
  }
  dv.setUint32(p, ((bsum << 16) | a) >>> 0);
  end(start, idatLen);

  chunk("IEND", 0);
  dv.setUint32(o + 8, crc32(out, o + 4, o + 8));
  return out;
}

function bytesToBase64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

/** Decodes a base64 ThumbHash (as in `asset.placeholder.thumbhash`) into a `data:image/png` URL. */
export function thumbHashToDataURL(thumbhash: string | Uint8Array): string {
  const bytes = typeof thumbhash === "string" ? base64ToBytes(thumbhash) : thumbhash;
  const { width, height, rgba } = thumbHashToRGBA(bytes);
  return "data:image/png;base64," + bytesToBase64(rgbaToPng(width, height, rgba));
}

/** Like {@link thumbHashToDataURL} but returns undefined for missing or malformed hashes. */
export function safeThumbHashToDataURL(thumbhash: string | null | undefined): string | undefined {
  if (!thumbhash) return undefined;
  try {
    return thumbHashToDataURL(thumbhash);
  } catch {
    return undefined;
  }
}
