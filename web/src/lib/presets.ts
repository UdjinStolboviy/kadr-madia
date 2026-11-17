import { t } from "@/i18n/store";

import { formatBytes } from "./format";

// Render preset params (docs/PIPELINES.md §1) in human terms. Params are an open
// object from the server, so every access is defensive.

type Obj = Record<string, unknown>;

function obj(v: unknown): Obj | null {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Obj) : null;
}
function num(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}
function str(v: unknown): string | null {
  return typeof v === "string" && v.length > 0 ? v : null;
}

export interface PresetFact {
  label: string;
  value: string;
}

export interface PresetSummary {
  /** One-line summary, e.g. "AVIF + WebP · ≤ 20 KB · max 800px · sharpen light". */
  headline: string;
  facts: PresetFact[];
}

function sharpenLabel(s: Obj | null): string | null {
  if (!s) return null;
  const sigma = num(s.sigma);
  if (sigma === null) return t("common.preset.sharpenWord");
  if (sigma <= 0.7) return t("common.preset.sharpenLight");
  if (sigma <= 1.2) return `${t("common.preset.sharpenWord")} ${sigma}`;
  return t("common.preset.sharpenStrong");
}

function imageSummary(p: Obj): PresetSummary {
  const parts: string[] = [];
  const facts: PresetFact[] = [];

  const outputs = Array.isArray(p.outputs) ? p.outputs.map(obj).filter((o): o is Obj => !!o) : [];
  const formats = [...new Set(outputs.map((o) => str(o.format)).filter((f): f is string => !!f))];
  if (formats.length) {
    const label = formats.map((f) => (f === "webp" ? "WebP" : f.toUpperCase())).join(" + ");
    parts.push(label);
    facts.push({ label: t("common.preset.formats"), value: label });
  }

  const q = obj(p.quality);
  if (q) {
    const mode = str(q.mode);
    if (mode === "budget" && num(q.maxBytes) !== null) {
      const b = formatBytes(num(q.maxBytes));
      parts.push(`≤ ${b}`);
      const qr = num(q.qMin) !== null && num(q.qMax) !== null ? ` (q ${q.qMin}–${q.qMax})` : "";
      facts.push({ label: t("common.preset.quality"), value: t("common.preset.byteBudget", { size: b, range: qr }) });
    } else if (mode === "fixed" && num(q.q) !== null) {
      parts.push(`q ${q.q}`);
      facts.push({ label: t("common.preset.quality"), value: t("common.preset.fixedQ", { q: String(q.q) }) });
    } else if (mode === "ssim" && num(q.target) !== null) {
      parts.push(`SSIM ${q.target}`);
      facts.push({ label: t("common.preset.quality"), value: t("common.preset.targetSsim", { target: String(q.target) }) });
    }
  }

  const r = obj(p.resize);
  if (r) {
    const w = num(r.maxWidth);
    const h = num(r.maxHeight);
    const crop = str(r.crop);
    const aspect = str(r.aspect);
    if (crop) {
      const size = w && h && w === h ? `${w}px` : w && h ? `${w}×${h}` : w ? `${w}px` : "";
      const label = `${t("common.preset.smartCrop")}${aspect ? ` ${aspect}` : ""}${size ? ` ${size}` : ""}`;
      parts.push(label);
      facts.push({ label: t("common.preset.resize"), value: `${label} (${crop})` });
    } else if (w || h) {
      const side = w && h && w === h ? `${w}px` : [w && `w ${w}`, h && `h ${h}`].filter(Boolean).join(" · ");
      parts.push(t("common.preset.maxSide", { side }));
      facts.push({ label: t("common.preset.resize"), value: t("common.preset.fitInside", { side }) });
    }
  }

  const c = obj(p.color);
  if (c) {
    const tags = [c.grayscale === true && t("common.preset.grayscale"), c.normalise === true && t("common.preset.normalise")].filter(
      (x): x is string => typeof x === "string",
    );
    if (tags.length) {
      parts.push(tags.join(" + "));
      facts.push({ label: t("common.preset.color"), value: tags.join(", ") });
    }
  }

  const sh = sharpenLabel(obj(p.sharpen));
  if (sh) {
    parts.push(sh);
    facts.push({ label: t("common.preset.sharpen"), value: sh.slice(t("common.preset.sharpenWord").length).trim() || t("common.preset.sharpenOn") });
  }

  const ph = obj(p.placeholder);
  if (ph) {
    const tags = [ph.thumbhash === true && "ThumbHash", ph.dominantColor === true && t("common.preset.dominantColor")].filter(
      (x): x is string => typeof x === "string",
    );
    if (tags.length) facts.push({ label: t("common.preset.placeholder"), value: tags.join(" + ") });
  }

  const lim = obj(p.limits);
  if (lim) {
    const items: string[] = [];
    if (num(lim.maxInputBytes) !== null) items.push(`≤ ${formatBytes(num(lim.maxInputBytes))}`);
    if (num(lim.maxInputPixels) !== null) items.push(`≤ ${t("common.preset.mp", { n: Math.round(num(lim.maxInputPixels)! / 1e6) })}`);
    if (Array.isArray(lim.accept)) items.push(lim.accept.filter((a) => typeof a === "string").join(", "));
    if (items.length) facts.push({ label: t("common.preset.input"), value: items.join(" · ") });
  }

  if (typeof p.retainOriginal === "boolean") {
    facts.push({ label: t("common.preset.original"), value: p.retainOriginal ? t("common.preset.retained") : t("common.preset.discarded") });
  }

  return { headline: parts.join(" · ") || t("common.preset.imageDefault"), facts };
}

function videoSummary(p: Obj): PresetSummary {
  const parts: string[] = [];
  const facts: PresetFact[] = [];
  const v = obj(p.video);
  if (v) {
    const codec = str(v.codec);
    if (codec) parts.push(codec === "h264" ? "H.264" : codec.toUpperCase());
    const w = num(v.maxWidth);
    if (w) parts.push(`≤ ${w}px`);
    if (num(v.crf) !== null) parts.push(`CRF ${v.crf}`);
    if (str(v.maxrate)) parts.push(`max ${v.maxrate}`);
    if (num(v.fps)) parts.push(`${v.fps} fps`);
    const vf = [
      codec && (codec === "h264" ? "H.264" : codec.toUpperCase()),
      num(v.crf) !== null && `CRF ${v.crf}`,
      str(v.maxrate) && `maxrate ${v.maxrate}`,
      str(v.preset) && `preset ${v.preset}`,
    ].filter(Boolean);
    facts.push({ label: t("common.preset.video"), value: vf.join(" · ") });
    if (w || num(v.fps)) {
      facts.push({ label: t("common.preset.frame"), value: [w && t("common.preset.maxWidth", { w }), num(v.fps) && `${v.fps} fps`].filter(Boolean).join(" · ") });
    }
    if (str(v.hw)) facts.push({ label: t("common.preset.hardware"), value: v.hw === "auto" ? t("common.preset.vaapiAuto") : String(v.hw) });
  }
  const a = obj(p.audio);
  if (a) {
    const ch = num(a.channels);
    const label = [
      str(a.codec)?.toUpperCase(),
      str(a.bitrate),
      ch === 1 ? t("common.preset.mono") : ch === 2 ? t("common.preset.stereo") : ch ? `${ch} ch` : null,
    ]
      .filter(Boolean)
      .join(" ");
    if (label) {
      parts.push(label);
      facts.push({ label: t("common.preset.audio"), value: label });
    }
  }
  const c = obj(p.container);
  if (c && str(c.format)) {
    facts.push({ label: t("common.preset.container"), value: `${String(c.format).toUpperCase()}${c.faststart ? " + faststart" : ""}` });
  }
  const poster = obj(p.poster);
  if (poster) {
    const mb = num(poster.maxBytes);
    if (mb) parts.push(t("common.preset.posterHeadline", { size: formatBytes(mb) }));
    facts.push({
      label: t("common.preset.poster"),
      value: [num(poster.atSec) !== null && t("common.preset.posterAt", { sec: String(poster.atSec) }), str(poster.preset), mb && `≤ ${formatBytes(mb)}`]
        .filter(Boolean)
        .join(" · "),
    });
  }
  const lim = obj(p.limits);
  if (lim) {
    const items: string[] = [];
    if (num(lim.maxInputBytes) !== null) items.push(`≤ ${formatBytes(num(lim.maxInputBytes))}`);
    if (num(lim.maxDurationSec) !== null) items.push(`≤ ${t("common.preset.minutes", { n: Math.round(num(lim.maxDurationSec)! / 60) })}`);
    if (num(lim.maxWidth) && num(lim.maxHeight)) items.push(`≤ ${lim.maxWidth}×${lim.maxHeight}`);
    if (items.length) facts.push({ label: t("common.preset.input"), value: items.join(" · ") });
  }
  if (typeof p.retainOriginal === "boolean") {
    facts.push({ label: t("common.preset.original"), value: p.retainOriginal ? t("common.preset.retained") : t("common.preset.discarded") });
  }
  return { headline: parts.join(" · ") || t("common.preset.videoDefault"), facts };
}

export function summarizePreset(kind: string, params: Record<string, unknown>): PresetSummary {
  try {
    const k = str(params.kind) ?? kind;
    return k === "video" ? videoSummary(params) : imageSummary(params);
  } catch {
    return { headline: t("common.preset.generic", { kind }), facts: [] };
  }
}

const MODE_NAMES = ["compact", "balanced", "quality"];

/** The six built-in compression-mode presets (image-<mode>, video-<mode>). */
export function isModeSlug(slug: string): boolean {
  return MODE_NAMES.some((m) => slug === `image-${m}` || slug === `video-${m}`);
}
