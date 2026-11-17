import { describe, expect, it } from "vitest";
import * as entry from "../src/worker.ts";

// Regression: workerd rejects modules exporting non-handler names (found when
// running under `wrangler dev --local` with index.ts as the entry).
describe("deployed entrypoint", () => {
  it("exports only a default handler with fetch()", () => {
    expect(Object.keys(entry)).toEqual(["default"]);
    expect(typeof entry.default.fetch).toBe("function");
  });
});
