import { intlLocale, t } from "@/i18n/store";

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const;

/** Binary (1024-based) byte formatting: 20480 → "20 KB". */
export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return "—";
  if (bytes === 0) return "0 B";
  const sign = bytes < 0 ? "-" : "";
  let v = Math.abs(bytes);
  let i = 0;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  const fixed = i === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : digits).replace(/\.0+$/, "");
  return `${sign}${fixed} ${UNITS[i]}`;
}

export const GB = 1024 ** 3;

export function bytesToGb(bytes: number | null | undefined): number | null {
  if (bytes === null || bytes === undefined) return null;
  return Math.round((bytes / GB) * 100) / 100;
}

export function gbToBytes(gb: number): number {
  return Math.round(gb * GB);
}

// Intl formatters are cached per active language.
const fmtCache = new Map<string, Intl.DateTimeFormat | Intl.RelativeTimeFormat>();
function dateFmt(): Intl.DateTimeFormat {
  const key = `d:${intlLocale()}`;
  let f = fmtCache.get(key) as Intl.DateTimeFormat | undefined;
  if (!f) {
    f = new Intl.DateTimeFormat(intlLocale(), { year: "numeric", month: "short", day: "numeric" });
    fmtCache.set(key, f);
  }
  return f;
}
function dateTimeFmt(): Intl.DateTimeFormat {
  const key = `dt:${intlLocale()}`;
  let f = fmtCache.get(key) as Intl.DateTimeFormat | undefined;
  if (!f) {
    f = new Intl.DateTimeFormat(intlLocale(), {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
    fmtCache.set(key, f);
  }
  return f;
}
function relFmt(): Intl.RelativeTimeFormat {
  const key = `r:${intlLocale()}`;
  let f = fmtCache.get(key) as Intl.RelativeTimeFormat | undefined;
  if (!f) {
    f = new Intl.RelativeTimeFormat(intlLocale(), { numeric: "auto" });
    fmtCache.set(key, f);
  }
  return f;
}

/** Locale-aware number formatting for the active language. */
export function formatNumber(n: number, opts?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(intlLocale(), opts).format(n);
}

function toDate(v: string | null | undefined): Date | null {
  if (!v) return null;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function formatDate(v: string | null | undefined): string {
  const d = toDate(v);
  return d ? dateFmt().format(d) : "—";
}

export function formatDateTime(v: string | null | undefined): string {
  const d = toDate(v);
  return d ? dateTimeFmt().format(d) : "—";
}

const STEPS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["second", 60],
  ["minute", 60],
  ["hour", 24],
  ["day", 7],
  ["week", 4.345],
  ["month", 12],
  ["year", Infinity],
];

export function formatRelative(v: string | null | undefined, now = Date.now()): string {
  const d = toDate(v);
  if (!d) return t("shell.fmt.never");
  let delta = (d.getTime() - now) / 1000;
  if (Math.abs(delta) < 30) return t("shell.fmt.justNow");
  for (const [unit, size] of STEPS) {
    if (Math.abs(delta) < size) return relFmt().format(Math.round(delta), unit);
    delta /= size;
  }
  return dateFmt().format(d);
}

export function formatDuration(totalSec: number): string {
  const s = Math.max(0, Math.floor(totalSec));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}${t("shell.fmt.d")} ${h}${t("shell.fmt.h")}`;
  if (h > 0) return `${h}${t("shell.fmt.h")} ${m}${t("shell.fmt.m")}`;
  if (m > 0) return `${m}${t("shell.fmt.m")} ${s % 60}${t("shell.fmt.s")}`;
  return `${s}${t("shell.fmt.s")}`;
}

export function formatMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ${t("shell.fmt.ms")}`;
  return `${formatNumber(ms / 1000, { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${t("shell.fmt.s")}`;
}
