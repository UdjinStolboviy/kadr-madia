/**
 * Type-level contract between the SDK surface and the generated OpenAPI
 * types. These assertions are checked by `pnpm typecheck` (and `pnpm test`):
 * if `api/openapi.yaml` changes shape, regenerate with `pnpm gen` and this
 * file (or the SDK code using the old shape) stops compiling.
 */
import { describe, expectTypeOf, it } from "vitest";
import type {
  Asset,
  Camelize,
  CreateUploadInput,
  NamespaceUsage,
  Page,
  WebhookEvent,
  WebhookEventType,
  components,
  paths,
} from "../src/index.js";
import { snakeToCamel } from "../src/case.js";
import type { CamelCase } from "../src/wire.js";

type S = components["schemas"];

describe("generated contract", () => {
  it("derives camelCase types from the wire schemas", () => {
    expectTypeOf<Asset>().toEqualTypeOf<Camelize<S["Asset"]>>();
    expectTypeOf<Asset["presetVersion"]>().toEqualTypeOf<number>();
    expectTypeOf<Asset["original"]["contentType"]>().toEqualTypeOf<string | null>();
    expectTypeOf<Asset["metadata"]>().toEqualTypeOf<S["Metadata"]>();
    expectTypeOf<NamespaceUsage>().toHaveProperty("usedBytes");
    expectTypeOf<CreateUploadInput>().toHaveProperty("contentType");
    expectTypeOf<Page<Asset>["items"]>().toEqualTypeOf<Asset[]>();
  });

  it("covers exactly the implemented endpoints", () => {
    expectTypeOf<keyof paths>().toEqualTypeOf<
      | "/v1/ping"
      | "/v1/uploads"
      | "/v1/uploads/{upload_id}/complete"
      | "/v1/uploads/{upload_id}"
      | "/v1/assets"
      | "/v1/assets/{asset_id}"
      | "/v1/presets"
      | "/v1/presets/{slug}"
      | "/v1/namespaces/{namespace}/usage"
      | "/v1/namespaces/{namespace}/quota"
    >();
  });

  it("types every webhook event", () => {
    expectTypeOf<WebhookEventType>().toEqualTypeOf<
      | "asset.ready"
      | "asset.failed"
      | "asset.updated"
      | "asset.deleted"
      | "upload.expired"
      | "quota.warning"
      | "quota.exceeded"
      | "webhook.test"
    >();
    expectTypeOf<Extract<WebhookEvent, { type: "asset.failed" }>["data"]["error"]["code"]>().toEqualTypeOf<
      S["AssetError"]["code"]
    >();
  });

  it("matches the runtime key conversion", () => {
    const key = "quota_video_sec_month" as const;
    const converted: CamelCase<typeof key> = "quotaVideoSecMonth";
    expectTypeOf(converted).toEqualTypeOf<"quotaVideoSecMonth">();
    if (snakeToCamel(key) !== converted) throw new Error("CamelCase<> and snakeToCamel disagree");
  });
});
