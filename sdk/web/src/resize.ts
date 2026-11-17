import { KadrAbortError } from "./errors.js";
import type { PreResizeOptions } from "./types.js";

const RESIZABLE = new Set(["image/jpeg", "image/png", "image/webp", "image/avif", "image/bmp"]);

/** Computes the target size, or null when the image already fits in `maxSide`. */
export function fitWithin(width: number, height: number, maxSide: number): { width: number; height: number } | null {
  const longest = Math.max(width, height);
  if (!(maxSide > 0) || longest <= maxSide) return null;
  const scale = maxSide / longest;
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) };
}

// The worker body is stringified, so it must not reference anything outside itself.
const WORKER_SOURCE = `
self.onmessage = async (e) => {
  const { id, blob, maxSide, quality, type } = e.data;
  try {
    const bmp = await createImageBitmap(blob);
    const longest = Math.max(bmp.width, bmp.height);
    if (!(maxSide > 0) || longest <= maxSide) { bmp.close(); self.postMessage({ id, blob: null }); return; }
    const s = maxSide / longest;
    const w = Math.max(1, Math.round(bmp.width * s)), h = Math.max(1, Math.round(bmp.height * s));
    const canvas = new OffscreenCanvas(w, h);
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("no 2d context");
    ctx.imageSmoothingQuality = "high";
    ctx.drawImage(bmp, 0, 0, w, h);
    bmp.close();
    const out = await canvas.convertToBlob({ type, quality });
    self.postMessage({ id, blob: out });
  } catch (err) {
    self.postMessage({ id, error: String(err && err.message || err) });
  }
};
`;

export function resizeSupported(): boolean {
  const g = globalThis as Record<string, unknown>;
  return (
    typeof g["Worker"] === "function" &&
    typeof g["OffscreenCanvas"] === "function" &&
    typeof g["createImageBitmap"] === "function" &&
    typeof g["URL"] === "function" &&
    typeof (URL as { createObjectURL?: unknown }).createObjectURL === "function"
  );
}

function pickType(input: string, forced: PreResizeOptions["outputType"]): string {
  if (forced) return forced;
  return input === "image/png" || input === "image/webp" ? input : "image/jpeg";
}

function extFor(type: string): string {
  return type === "image/png" ? "png" : type === "image/webp" ? "webp" : "jpg";
}

export interface ResizedFile {
  blob: Blob;
  filename: string;
  contentType: string;
  resized: boolean;
}

/**
 * Downscales an image in a Web Worker with OffscreenCanvas. Returns the
 * original untouched when resizing is unsupported, not applicable (not a
 * raster image, already small enough) or fails for any reason, so callers can
 * always upload the result.
 */
export async function preResizeImage(
  file: Blob,
  filename: string,
  opts: PreResizeOptions = {},
  signal?: AbortSignal,
): Promise<ResizedFile> {
  const original: ResizedFile = { blob: file, filename, contentType: file.type, resized: false };
  if (!RESIZABLE.has(file.type) || !resizeSupported()) return original;
  if (signal?.aborted) throw new KadrAbortError();

  const type = pickType(file.type, opts.outputType);
  const url = URL.createObjectURL(new Blob([WORKER_SOURCE], { type: "text/javascript" }));
  let worker: Worker | undefined;
  try {
    worker = new Worker(url);
    const w = worker;
    const out = await new Promise<Blob | null>((resolve, reject) => {
      const onAbort = () => {
        reject(new KadrAbortError());
      };
      signal?.addEventListener("abort", onAbort, { once: true });
      w.onmessage = (e: MessageEvent<{ blob?: Blob | null; error?: string }>) => {
        signal?.removeEventListener("abort", onAbort);
        if (e.data.error !== undefined) reject(new Error(e.data.error));
        else resolve(e.data.blob ?? null);
      };
      w.onerror = (e) => {
        signal?.removeEventListener("abort", onAbort);
        reject(new Error(e.message || "worker error"));
      };
      w.postMessage({ id: 1, blob: file, maxSide: opts.maxSide ?? 2048, quality: opts.quality ?? 0.9, type });
    });
    if (!out) return original;
    return {
      blob: out,
      filename: filename.replace(/\.[^./\\]+$/, "") + "." + extFor(out.type || type),
      contentType: out.type || type,
      resized: true,
    };
  } catch (err) {
    if (err instanceof KadrAbortError) throw err;
    return original; // any other failure: fall back to the original bytes
  } finally {
    worker?.terminate();
    URL.revokeObjectURL(url);
  }
}
