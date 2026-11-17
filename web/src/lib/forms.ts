import { z } from "zod";

import { t } from "@/i18n/store";

import type { StorageProvider } from "./types";

/** Select value meaning "no storage profile" (Radix Select disallows ""). */
export const NO_STORAGE = "__none";

export const quotaSchema = z
  .string()
  .trim()
  .refine((v) => v === "" || (/^\d+(\.\d+)?$/.test(v) && Number(v) > 0), { error: () => t("common.form.quota") });

export const PROVIDER_LABEL: Record<StorageProvider, string> = {
  r2: "Cloudflare R2",
  s3: "Amazon S3",
  b2: "Backblaze B2",
  hetzner: "Hetzner Object Storage",
  get garage() {
    return t("common.provider.garage");
  },
};

export function validateOrigin(tag: string): string | null {
  if (tag === "*") return null;
  try {
    const u = new URL(tag);
    if (u.protocol !== "http:" && u.protocol !== "https:") return t("common.form.originScheme");
    if (u.pathname !== "/" || u.search || u.hash) return t("common.form.originPath");
    if (tag.endsWith("/")) return t("common.form.originSlash");
    return null;
  } catch {
    return t("common.form.originInvalid", { tag });
  }
}
