export type RangeResult =
  | { kind: "none" }
  | { kind: "unsatisfiable" }
  | { kind: "range"; start: number; end: number }; // inclusive

/** Parses a single-range `Range: bytes=` header. Multi-range or invalid syntax is ignored (full 200). */
export function parseRange(header: string | null, size: number): RangeResult {
  if (!header) return { kind: "none" };
  const m = /^bytes=(\d*)-(\d*)$/.exec(header.trim());
  if (!m) return { kind: "none" };
  const [, a = "", b = ""] = m;
  if (a === "" && b === "") return { kind: "none" };
  if (a === "") {
    const n = Number(b);
    if (n === 0 || size === 0) return { kind: "unsatisfiable" };
    return { kind: "range", start: Math.max(0, size - n), end: size - 1 };
  }
  const start = Number(a);
  let end = b === "" ? size - 1 : Number(b);
  if (b !== "" && end < start) return { kind: "none" }; // invalid per RFC 9110
  if (start >= size) return { kind: "unsatisfiable" };
  end = Math.min(end, size - 1);
  return { kind: "range", start, end };
}
