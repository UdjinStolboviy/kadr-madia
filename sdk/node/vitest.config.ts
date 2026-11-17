import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    // End-to-end tests need a running Kadr; run them with `pnpm test:e2e`
    // (see scripts/e2e-sdk.sh).
    exclude: ["test/e2e/**", "node_modules/**"],
    environment: "node",
  },
});
