// Deployed entrypoint. The Workers runtime rejects a module whose named exports
// are not handlers/classes ("Incorrect type for map entry ..."), while index.ts
// also exports helpers (handle, contentDisposition, ...) for unit tests. This
// shim re-exports only the default handler.
export { default } from "./index.ts";
