/**
 * Key-case conversion between the SDK (camelCase) and the wire (snake_case).
 *
 * Conversion is recursive over plain objects and arrays, except for the values
 * of keys listed in `OPAQUE_KEYS`: those values are copied verbatim, because
 * they contain user data (`metadata`), protocol data (`headers`, e.g.
 * `Content-Type`) or preset parameters (`params`, already camelCase on the
 * wire) whose keys must not change. Keep in sync with `OpaqueKey` in
 * `wire.ts`.
 */

import type { OpaqueKey } from "./wire.js";

// A Record (not an array) so adding a key to `OpaqueKey` fails typecheck here.
const OPAQUE: Record<OpaqueKey, true> = { metadata: true, headers: true, params: true };
const OPAQUE_KEYS = new Set<string>(Object.keys(OPAQUE));

export function camelToSnake(key: string): string {
  return key.replace(/[A-Z]/g, (c) => `_${c.toLowerCase()}`);
}

export function snakeToCamel(key: string): string {
  return key.replace(/_([a-z0-9])/g, (_, c: string) => c.toUpperCase());
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object") return false;
  const proto = Object.getPrototypeOf(value) as unknown;
  return proto === Object.prototype || proto === null;
}

function convert(value: unknown, mapKey: (k: string) => string): unknown {
  if (Array.isArray(value)) return value.map((v) => convert(v, mapKey));
  if (!isPlainObject(value)) return value;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(value)) {
    if (v === undefined) continue;
    out[mapKey(k)] = OPAQUE_KEYS.has(k) ? v : convert(v, mapKey);
  }
  return out;
}

/** Converts SDK input (camelCase) to the wire format (snake_case). Drops `undefined` values. */
export function toWire(value: unknown): unknown {
  return convert(value, camelToSnake);
}

/** Converts a wire response (snake_case) to the SDK format (camelCase). */
// The type parameter is a convenience cast for callers.
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters
export function fromWire<T>(value: unknown): T {
  return convert(value, snakeToCamel) as T;
}
