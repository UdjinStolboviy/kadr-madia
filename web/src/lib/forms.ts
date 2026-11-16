import { z } from "zod";

import type { StorageProvider } from "./types";

/** Select value meaning "no storage profile" (Radix Select disallows ""). */
export const NO_STORAGE = "__none";

export const quotaSchema = z
  .string()
  .trim()
  .refine((v) => v === "" || (/^\d+(\.\d+)?$/.test(v) && Number(v) > 0), "Enter a positive number of GB, or leave empty");

export const PROVIDER_LABEL: Record<StorageProvider, string> = {
  r2: "Cloudflare R2",
  s3: "Amazon S3",
  b2: "Backblaze B2",
  hetzner: "Hetzner Object Storage",
  garage: "Garage (self-hosted)",
};

export function validateOrigin(tag: string): string | null {
  if (tag === "*") return null;
  try {
    const u = new URL(tag);
    if (u.protocol !== "http:" && u.protocol !== "https:") return "Origin must use http or https";
    if (u.pathname !== "/" || u.search || u.hash) return "Origin must not include a path, e.g. https://app.example.com";
    if (tag.endsWith("/")) return "Remove the trailing slash";
    return null;
  } catch {
    return `"${tag}" is not a valid origin (e.g. https://app.example.com)`;
  }
}
