import { en } from "./en";
import { es } from "./es";
import { uk } from "./uk";
import type { Dict, Locale, Messages } from "./core";

export type MessageKey = keyof typeof en;

// Compile-time parity across the merged dictionaries (per-namespace files enforce it too).
const _uk: Messages<typeof en> = uk;
const _es: Messages<typeof en> = es;
void _uk;
void _es;

export const dictionaries: Record<Locale, Dict> = { en, uk, es };
