/**
 * Key-case conversion between the SDK (camelCase) and the wire (snake_case).
 *
 * Conversion is recursive over plain objects and arrays, except for the values
 * of keys listed in `OPAQUE_KEYS`: those values are copied verbatim, because
 * they contain user data (`metadata`) or protocol data (`headers`, e.g.
 * `Content-Type`) whose keys must not change.
 */

const OPAQUE_KEYS = new Set(["metadata", "headers"]);

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
