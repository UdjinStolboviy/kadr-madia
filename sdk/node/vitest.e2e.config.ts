import { defineConfig } from "vitest/config";

// Contract tests against a real Kadr server started by scripts/e2e-sdk.sh.
export default defineConfig({
  test: {
    include: ["test/e2e/**/*.test.ts"],
    environment: "node",
    testTimeout: 60_000,
    hookTimeout: 60_000,
    fileParallelism: false,
  },
});
