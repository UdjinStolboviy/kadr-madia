import type { KadrAssetLike, KadrRendition, ResolveUrl } from "./types.js";

const defaultResolve: ResolveUrl = (r) => r.url;

export interface ImageSource {
  format: string;
  type: string;
  srcSet: string;
}

export interface ImagePlan {
  sources: ImageSource[];
  src: string | undefined;
  width: number | undefined;
  height: number | undefined;
}

const MIME: Record<string, string> = {
  avif: "image/avif",
  webp: "image/webp",
  jpeg: "image/jpeg",
  jpg: "image/jpeg",
  png: "image/png",
  gif: "image/gif",
  mp4: "video/mp4",
  webm: "video/webm",
};

export function mimeFor(format: string): string {
  const f = format.toLowerCase();
  return MIME[f] ?? (f.includes("/") ? f : `image/${f}`);
}

// Most modern first; the last entry of the list becomes the <img> fallback.
const IMAGE_ORDER = ["avif", "webp", "png", "jpeg", "jpg", "gif"];

function rank(format: string): number {
  const i = IMAGE_ORDER.indexOf(format.toLowerCase());
  return i === -1 ? IMAGE_ORDER.length : i;
}

/** Builds `<picture>` sources (one srcset per format, w descriptors) plus a fallback `src`. */
export function planImage(asset: KadrAssetLike, resolve: ResolveUrl = defaultResolve): ImagePlan {
  const byFormat = new Map<string, { r: KadrRendition; url: string }[]>();
  for (const r of asset.renditions) {
    const url = resolve(r);
    if (!url) continue;
    const f = r.format.toLowerCase();
    const list = byFormat.get(f) ?? [];
    list.push({ r, url });
    byFormat.set(f, list);
  }
  const formats = [...byFormat.keys()].sort((a, b) => rank(a) - rank(b));
  const sources: ImageSource[] = formats.map((format) => {
    const list = byFormat.get(format) ?? [];
    const seen = new Set<number>();
    const parts: string[] = [];
    for (const { r, url } of [...list].sort((a, b) => (a.r.width ?? 0) - (b.r.width ?? 0))) {
      if (r.width && !seen.has(r.width)) {
        seen.add(r.width);
        parts.push(`${url} ${r.width}w`);
      }
    }
    // Without widths there is nothing to describe: a plain URL list is a single candidate.
    const srcSet = parts.length > 0 ? parts.join(", ") : (list[0]?.url ?? "");
    return { format, type: mimeFor(format), srcSet };
  });

  // Fallback <img>: the widest rendition of the most compatible format.
  const fallbackFormat = [...formats].reverse().find((f) => f !== "avif") ?? formats[0];
  const fallbackList = (fallbackFormat ? byFormat.get(fallbackFormat) : undefined) ?? [];
  const largest = [...fallbackList].sort((a, b) => (b.r.width ?? 0) - (a.r.width ?? 0))[0];

  const dims = largest?.r;
  const width = dims?.width ?? asset.original?.width ?? undefined;
  const height = dims?.height ?? asset.original?.height ?? undefined;
  return { sources, src: largest?.url, width: width ?? undefined, height: height ?? undefined };
}

/**
 * The `<source type>` of a video rendition. With the codec in it a browser
 * that cannot decode AV1 (e.g. older iPhones) skips the source and falls back
 * to the next one instead of showing a broken player.
 */
export function videoType(r: KadrRendition): string {
  const base = mimeFor(r.format);
  if (r.format.toLowerCase() !== "mp4") return base;
  switch (r.encoder) {
    case "libsvtav1":
      return `${base}; codecs="av01.0.08M.08, mp4a.40.2"`;
    case "libx264":
    case "h264_vaapi":
      return `${base}; codecs="avc1.640029, mp4a.40.2"`;
    default:
      return base;
  }
}

export interface VideoPlan {
  sources: { src: string; type: string }[];
  poster: string | undefined;
  width: number | undefined;
  height: number | undefined;
}

const VIDEO_FORMATS = new Set(["mp4", "webm", "mov", "m4v"]);

export function planVideo(asset: KadrAssetLike, resolve: ResolveUrl = defaultResolve): VideoPlan {
  const vids = asset.renditions.filter((r) => VIDEO_FORMATS.has(r.format.toLowerCase()));
  const ordered = [...vids].sort((a, b) => {
    const wa = a.format.toLowerCase() === "mp4" ? 0 : 1;
    const wb = b.format.toLowerCase() === "mp4" ? 0 : 1;
    // AV1 first (smaller); browsers without it skip to the H.264 source.
    const ca = a.encoder === "libsvtav1" ? 0 : 1;
    const cb = b.encoder === "libsvtav1" ? 0 : 1;
    return wa - wb || ca - cb || (b.width ?? 0) - (a.width ?? 0);
  });
  const sources: { src: string; type: string }[] = [];
  for (const r of ordered) {
    const src = resolve(r);
    if (src) sources.push({ src, type: videoType(r) });
  }
  const first = ordered[0];
  const poster = asset.poster ? resolve(asset.poster) : undefined;
  const width = first?.width ?? asset.poster?.width ?? asset.original?.width ?? undefined;
  const height = first?.height ?? asset.poster?.height ?? asset.original?.height ?? undefined;
  return { sources, poster, width: width ?? undefined, height: height ?? undefined };
}
