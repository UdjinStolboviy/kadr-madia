import { defineConfig } from "vitest/config";

// Spawns `wrangler dev --local` (workerd). Run with `pnpm test:integration`.
export default defineConfig({
  test: {
    include: ["test/integration/**/*.itest.ts"],
    environment: "node",
    testTimeout: 30_000,
    hookTimeout: 120_000,
    fileParallelism: false,
  },
});
