import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: {
    // tsup's DTS bundler injects the `baseUrl` option, which TypeScript 6 flags as deprecated.
    compilerOptions: { ignoreDeprecations: "6.0" },
  },
  sourcemap: true,
  clean: true,
  target: "es2022",
  platform: "browser",
  external: ["react", "react/jsx-runtime"],
  splitting: false,
  treeshake: true,
});
