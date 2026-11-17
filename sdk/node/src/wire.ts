/**
 * Type-level bridge between the generated OpenAPI types (snake_case wire
 * format, `src/generated/openapi.ts`) and the SDK surface (camelCase).
 *
 * Every public SDK type is derived from the generated schemas through
 * `Camelize`, so a change to `api/openapi.yaml` that renames, removes or
 * retypes a field breaks `pnpm typecheck` wherever the SDK (or its tests)
 * relies on the old shape. Regenerate with `pnpm gen`.
 */
import type { components, operations, paths } from "./generated/openapi.js";

export type { components, operations, paths };

/** Wire (snake_case) schemas from `api/openapi.yaml`. */
export type Schemas = components["schemas"];

/**
 * Keys whose values are passed through verbatim by the runtime conversion in
 * `case.ts` (user data, protocol headers, preset parameters). Must stay in
 * sync with `OPAQUE_KEYS` there.
 */
export type OpaqueKey = "metadata" | "headers" | "params";

/** `"preset_version"` → `"presetVersion"` (mirrors `snakeToCamel`). */
export type CamelCase<S extends string> = S extends `${infer Head}_${infer Tail}`
  ? `${Head}${Capitalize<CamelCase<Tail>>}`
  : S;

/** Recursively camelCases object keys, leaving `OpaqueKey` values untouched. */
export type Camelize<T> = T extends readonly (infer U)[]
  ? Camelize<U>[]
  : T extends object
    ? { [K in keyof T as K extends string ? CamelCase<K> : K]: K extends OpaqueKey ? T[K] : Camelize<T[K]> }
    : T;

/** Names of `{param}` placeholders in an OpenAPI path template. */
export type PathParamNames<S extends string> = S extends `${string}{${infer Name}}${infer Rest}`
  ? Name | PathParamNames<Rest>
  : never;

/** A path of the implemented `/v1` API (see `api/openapi.yaml`). */
export type ApiPath = keyof paths;

/** JSON body of a successful response of an operation. */
export type JsonResponse<
  Op extends keyof operations,
  Status extends keyof operations[Op]["responses"],
> = operations[Op]["responses"][Status] extends { content: { "application/json": infer B } } ? B : never;
