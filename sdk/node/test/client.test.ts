import { afterEach, describe, expect, it, vi } from "vitest";
import {
  Kadr,
  KadrError,
  KadrNetworkError,
  KadrTimeoutError,
  type FetchLike,
  type KadrOptions,
  type components,
} from "../src/index.js";

interface Call {
  url: string;
  init: RequestInit;
}

type Reply = Response | Error | ((call: Call) => Response | Promise<Response>);

function json(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });
}

function problem(body: unknown, status: number, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/problem+json", ...headers },
  });
}

function mockFetch(...replies: Reply[]) {
  const calls: Call[] = [];
  const fetch = vi.fn<FetchLike>(async (url, init) => {
    const call = { url, init };
    calls.push(call);
    const reply = replies.shift();
    if (!reply) throw new Error(`unexpected request: ${init.method ?? "GET"} ${url}`);
    if (reply instanceof Error) throw reply;
    return typeof reply === "function" ? reply(call) : reply;
  });
  return { fetch, calls };
}

function client(fetch: FetchLike, extra: Partial<KadrOptions> = {}) {
  return new Kadr({
    baseUrl: "http://kadr.internal:8080",
    apiKey: "kd_test_abc",
    fetch,
    retryBaseDelayMs: 1,
    ...extra,
  });
}

const wireAsset = (id: string): components["schemas"]["Asset"] => ({
  id,
  status: "ready",
  kind: "image",
  namespace: "org_8f2c",
  visibility: "private",
  preset: "photo-lite",
  preset_version: 3,
  original: { filename: "a.jpg", content_type: "image/jpeg", bytes: 10, width: 1, height: 1 },
  renditions: [{ name: "main", format: "avif", bytes: 5, width: 1, height: 1, key: `acme/blb_${id}/main.avif` }],
  placeholder: { thumbhash: "abc", dominant_color: "#a3866c" },
  duration_sec: null,
  poster: null,
  metadata: { entity: "receipt", entity_id: "rcp_123", nestedObj: { some_key: 1 } },
  savings_ratio: 0.9,
  created_at: "2026-11-16T20:59:12Z",
  ready_at: "2026-11-16T20:59:13Z",
});

afterEach(() => {
  vi.useRealTimers();
});

describe("base URL and auth", () => {
  it.each([
    "http://kadr.internal:8080",
    "http://kadr.internal:8080/",
    "http://kadr.internal:8080/v1",
    "http://kadr.internal:8080/v1/",
  ])("normalizes %s", async (baseUrl) => {
    const { fetch, calls } = mockFetch(json(wireAsset("ast_1")));
    await client(fetch, { baseUrl }).assets.get("ast_1");
    expect(calls[0]!.url).toBe("http://kadr.internal:8080/v1/assets/ast_1");
    const headers = calls[0]!.init.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer kd_test_abc");
  });
});

describe("case conversion", () => {
  it("converts camelCase input to snake_case and leaves metadata untouched", async () => {
    const { fetch, calls } = mockFetch(
      json(
        {
          upload_id: "upl_1",
          method: "PUT",
          url: "https://storage.example.com/tmp/upl_1",
          headers: { "Content-Type": "image/jpeg", "Content-Length": "384211" },
          expires_at: "2026-11-16T21:15:00Z",
        },
        201,
      ),
    );
    const res = await client(fetch).uploads.create(
      {
        preset: "photo-lite",
        namespace: "org_8f2c",
        visibility: "private",
        contentType: "image/jpeg",
        size: 384211,
        sha256: "9f86d0",
        metadata: { entity: "receipt", entityId: "rcp_123" },
      },
      { idempotencyKey: "user_1:client_1" },
    );
    const sent = JSON.parse(calls[0]!.init.body as string) as unknown;
    expect(sent).toEqual({
      preset: "photo-lite",
      namespace: "org_8f2c",
      visibility: "private",
      content_type: "image/jpeg",
      size: 384211,
      sha256: "9f86d0",
      metadata: { entity: "receipt", entityId: "rcp_123" },
    });
    expect((calls[0]!.init.headers as Record<string, string>)["Idempotency-Key"]).toBe("user_1:client_1");
    expect(res).toEqual({
      kind: "upload",
      uploadId: "upl_1",
      method: "PUT",
      url: "https://storage.example.com/tmp/upl_1",
      headers: { "Content-Type": "image/jpeg", "Content-Length": "384211" },
      expiresAt: "2026-11-16T21:15:00Z",
    });
  });

  it("converts snake_case responses to camelCase and leaves metadata untouched", async () => {
    const { fetch } = mockFetch(json(wireAsset("ast_1")));
    const asset = await client(fetch).assets.get("ast_1");
    expect(asset.presetVersion).toBe(3);
    expect(asset.original.contentType).toBe("image/jpeg");
    expect(asset.placeholder?.dominantColor).toBe("#a3866c");
    expect(asset.savingsRatio).toBe(0.9);
    expect(asset.metadata).toEqual({ entity: "receipt", entity_id: "rcp_123", nestedObj: { some_key: 1 } });
  });
});

describe("uploads", () => {
  it("rejects a 200 without an asset wrapper", async () => {
    const { fetch } = mockFetch(json(wireAsset("ast_bare")));
    await expect(
      client(fetch).uploads.create({
        preset: "photo-lite",
        namespace: "org_8f2c",
        visibility: "public",
        contentType: "image/jpeg",
        size: 1,
      }),
    ).rejects.toMatchObject({ code: "invalid_response" });
  });

  it("returns a deduplicated asset on 200 {asset}", async () => {
    const { fetch } = mockFetch(json({ asset: wireAsset("ast_d1") }));
    const res = await client(fetch).uploads.create({
      preset: "photo-lite",
      namespace: "org_8f2c",
      visibility: "private",
      contentType: "image/jpeg",
      size: 1,
      sha256: "ab",
    });
    expect(res.kind === "asset" && res.asset.id).toBe("ast_d1");
  });

  it("completes with wait and aborts", async () => {
    const { fetch, calls } = mockFetch(
      json(wireAsset("ast_1")),
      json(wireAsset("ast_1")),
      new Response(null, { status: 204 }),
    );
    const kadr = client(fetch);
    const asset = await kadr.uploads.complete("upl_1", { wait: "5s" });
    expect(asset.id).toBe("ast_1");
    expect(calls[0]!.url).toBe("http://kadr.internal:8080/v1/uploads/upl_1/complete?wait=5s");
    expect(calls[0]!.init.body).toBeUndefined();

    await kadr.uploads.complete("upl_1", { wait: 10 });
    expect(calls[1]!.url).toBe("http://kadr.internal:8080/v1/uploads/upl_1/complete?wait=10s");

    await kadr.uploads.abort("upl_1");
    expect(calls[2]!.init.method).toBe("DELETE");
    expect(calls[2]!.url).toBe("http://kadr.internal:8080/v1/uploads/upl_1");
  });
});

describe("pagination", () => {
  it("iterates across 3 pages", async () => {
    const { fetch, calls } = mockFetch(
      json({ items: [wireAsset("ast_1"), wireAsset("ast_2")], next_cursor: "c2" }),
      json({ items: [wireAsset("ast_3")], next_cursor: "c3" }),
      json({ items: [wireAsset("ast_4")], next_cursor: null }),
    );
    const ids: string[] = [];
    for await (const asset of client(fetch).assets.list({ namespace: "org_8f2c", limit: 2 })) {
      ids.push(asset.id);
    }
    expect(ids).toEqual(["ast_1", "ast_2", "ast_3", "ast_4"]);
    expect(calls.map((c) => c.url)).toEqual([
      "http://kadr.internal:8080/v1/assets?namespace=org_8f2c&limit=2",
      "http://kadr.internal:8080/v1/assets?namespace=org_8f2c&limit=2&cursor=c2",
      "http://kadr.internal:8080/v1/assets?namespace=org_8f2c&limit=2&cursor=c3",
    ]);
  });

  it("listPage returns one page with nextCursor", async () => {
    const { fetch } = mockFetch(json({ items: [wireAsset("ast_1")], next_cursor: "c2" }));
    const page = await client(fetch).assets.listPage({ status: "ready" });
    expect(page.items.map((a) => a.id)).toEqual(["ast_1"]);
    expect(page.nextCursor).toBe("c2");
  });

  it("rejects a list body without items", async () => {
    const { fetch } = mockFetch(json({ data: [wireAsset("ast_1")], next_cursor: null }));
    await expect(client(fetch).assets.listPage()).rejects.toMatchObject({ code: "invalid_response" });
  });
});

describe("errors", () => {
  it("parses problem+json into KadrError", async () => {
    const { fetch } = mockFetch(
      problem(
        {
          type: "https://kadr.dev/problems/quota-exceeded",
          title: "Quota exceeded",
          status: 402,
          detail: "Namespace org_8f2c has reached its storage limit",
          code: "quota_exceeded",
          errors: { size: "too big", content_type: "required" },
          instance: "/v1/uploads",
        },
        402,
        { "x-request-id": "req_123" },
      ),
    );
    const err = await client(fetch)
      .uploads.create({ preset: "p", namespace: "n", visibility: "public", contentType: "image/png", size: 1 })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(KadrError);
    const e = err as KadrError;
    expect(e.status).toBe(402);
    expect(e.code).toBe("quota_exceeded");
    expect(e.title).toBe("Quota exceeded");
    expect(e.detail).toBe("Namespace org_8f2c has reached its storage limit");
    expect(e.type).toBe("https://kadr.dev/problems/quota-exceeded");
    expect(e.instance).toBe("/v1/uploads");
    expect(e.errors).toEqual({ size: "too big", content_type: "required" });
    expect(e.requestId).toBe("req_123");
    expect(e.message).toContain("quota_exceeded");
  });

  it("produces a KadrError for non-JSON error bodies", async () => {
    const { fetch } = mockFetch(new Response("Bad Gateway from proxy", { status: 502, statusText: "Bad Gateway" }));
    const err = (await client(fetch, { maxRetries: 0 })
      .assets.get("ast_1")
      .catch((e: unknown) => e)) as KadrError;
    expect(err).toBeInstanceOf(KadrError);
    expect(err.status).toBe(502);
    expect(err.code).toBe("http_502");
    expect(err.detail).toBe("Bad Gateway from proxy");
  });

  it("maps network failures to KadrNetworkError", async () => {
    const { fetch } = mockFetch(new TypeError("fetch failed"));
    const err = await client(fetch, { maxRetries: 0 }).assets.get("ast_1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(KadrNetworkError);
    expect((err as KadrError).code).toBe("network_error");
  });

  it("maps timeouts to KadrTimeoutError", async () => {
    const hang: Reply = ({ init }) =>
      new Promise<Response>((_, reject) => {
        init.signal?.addEventListener("abort", () => {
          reject(new DOMException("aborted", "AbortError"));
        });
      });
    const { fetch } = mockFetch(hang);
    const err = await client(fetch, { maxRetries: 0, timeoutMs: 20 }).assets.get("ast_1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(KadrTimeoutError);
    expect((err as KadrError).code).toBe("timeout");
  });
});

describe("retries", () => {
  it("retries GET on 503", async () => {
    const { fetch } = mockFetch(problem({ code: "unavailable" }, 503), json(wireAsset("ast_1")));
    const asset = await client(fetch).assets.get("ast_1");
    expect(asset.id).toBe("ast_1");
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("retries GET on network errors and gives up after maxRetries", async () => {
    const { fetch } = mockFetch(new TypeError("a"), new TypeError("b"), new TypeError("c"));
    await expect(client(fetch, { maxRetries: 2 }).assets.get("ast_1")).rejects.toBeInstanceOf(KadrNetworkError);
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it("retries GET on 429 honoring Retry-After", async () => {
    vi.useFakeTimers();
    const { fetch } = mockFetch(
      problem({ code: "rate_limited" }, 429, { "retry-after": "2" }),
      json(wireAsset("ast_1")),
    );
    const promise = client(fetch).assets.get("ast_1");
    await vi.advanceTimersByTimeAsync(1999);
    expect(fetch).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetch).toHaveBeenCalledTimes(2);
    await expect(promise).resolves.toMatchObject({ id: "ast_1" });
  });

  it("does not retry POST without an idempotency key", async () => {
    const { fetch } = mockFetch(problem({ code: "unavailable" }, 503), json(wireAsset("ast_1")));
    await expect(client(fetch).uploads.complete("upl_1")).rejects.toMatchObject({ status: 503 });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("retries POST with an idempotency key", async () => {
    const { fetch, calls } = mockFetch(problem({ code: "unavailable" }, 503), json(wireAsset("ast_1")));
    await client(fetch).uploads.complete("upl_1", { wait: 5, idempotencyKey: "k1" });
    expect(fetch).toHaveBeenCalledTimes(2);
    for (const c of calls) {
      expect((c.init.headers as Record<string, string>)["Idempotency-Key"]).toBe("k1");
    }
  });

  it("retries a keyed POST on 409 idempotency_in_progress only", async () => {
    const { fetch } = mockFetch(
      problem({ code: "idempotency_in_progress" }, 409),
      json(wireAsset("ast_1")),
      problem({ code: "upload_aborted" }, 409),
    );
    const kadr = client(fetch);
    await expect(kadr.uploads.complete("upl_1", { idempotencyKey: "k1" })).resolves.toMatchObject({ id: "ast_1" });
    await expect(kadr.uploads.complete("upl_2", { idempotencyKey: "k2" })).rejects.toMatchObject({
      code: "upload_aborted",
    });
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it("does not retry PATCH, PUT or DELETE", async () => {
    const { fetch } = mockFetch(
      problem({ code: "unavailable" }, 503),
      problem({ code: "unavailable" }, 503),
      new TypeError("fetch failed"),
    );
    const kadr = client(fetch);
    await expect(kadr.assets.update("ast_1", { visibility: "public" })).rejects.toMatchObject({ status: 503 });
    await expect(kadr.namespaces.setQuota("org_8f2c", { quotaBytes: 1 })).rejects.toMatchObject({ status: 503 });
    await expect(kadr.assets.delete("ast_1")).rejects.toBeInstanceOf(KadrNetworkError);
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it("does not retry 4xx errors", async () => {
    const { fetch } = mockFetch(problem({ code: "not_found" }, 404));
    await expect(client(fetch).assets.get("ast_x")).rejects.toMatchObject({ code: "not_found" });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe("assets.upload (multipart/form-data)", () => {
  it("builds FormData from a Buffer", async () => {
    const { fetch, calls } = mockFetch(json(wireAsset("ast_1"), 201));
    await client(fetch).assets.upload(Buffer.from("hello"), {
      preset: "doc-lite",
      namespace: "org_8f2c",
      visibility: "private",
      filename: "scan.jpg",
      contentType: "image/jpeg",
      metadata: { entity: "receipt", entityId: "rcp_1" },
      wait: 5,
    });
    const { url, init } = calls[0]!;
    expect(url).toBe("http://kadr.internal:8080/v1/assets?wait=5s");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
    const form = init.body as FormData;
    expect(form).toBeInstanceOf(FormData);
    expect(form.get("preset")).toBe("doc-lite");
    expect(form.get("namespace")).toBe("org_8f2c");
    expect(form.get("visibility")).toBe("private");
    expect(form.get("filename")).toBe("scan.jpg");
    expect(form.get("content_type")).toBeNull();
    expect(form.get("sha256")).toBeNull();
    expect(JSON.parse(form.get("metadata") as string)).toEqual({ entity: "receipt", entityId: "rcp_1" });
    const file = form.get("file") as File;
    expect(file.name).toBe("scan.jpg");
    expect(file.type).toBe("image/jpeg");
    expect(await file.text()).toBe("hello");
  });

  it("buffers a ReadableStream and enforces the 25 MB limit", async () => {
    const { fetch, calls } = mockFetch(json(wireAsset("ast_1"), 201));
    const kadr = client(fetch);
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("ab"));
        controller.enqueue(new TextEncoder().encode("cd"));
        controller.close();
      },
    });
    await kadr.assets.upload(stream, { preset: "doc-lite", namespace: "n", visibility: "public" });
    const file = (calls[0]!.init.body as FormData).get("file") as File;
    expect(await file.text()).toBe("abcd");

    const big = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array(20 * 1024 * 1024));
        controller.enqueue(new Uint8Array(6 * 1024 * 1024));
        controller.close();
      },
    });
    await expect(
      kadr.assets.upload(big, { preset: "doc-lite", namespace: "n", visibility: "public" }),
    ).rejects.toMatchObject({ code: "too_large" });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe("presets, namespaces and ping", () => {
  it("calls the right endpoints and maps the wire shapes", async () => {
    const wirePreset = {
      slug: "photo-lite",
      kind: "image",
      description: "Cards",
      version: 3,
      params: { kind: "image", limits: { maxInputBytes: 1000, snake_key: 1 } },
      created_at: "2026-01-01T00:00:00Z",
    };
    const wireUsage = {
      namespace: "org_8f2c",
      used_bytes: 10,
      reserved_bytes: 5,
      asset_count: 2,
      video_sec_month: 90,
      quota_bytes: 1000,
      quota_assets: null,
      quota_video_sec_month: null,
    };
    const { fetch, calls } = mockFetch(
      json({ items: [wirePreset] }),
      json(wirePreset),
      json(wireUsage),
      json(wireUsage),
      json({
        ok: true,
        project: "acme",
        project_id: "prj_1",
        environment: "test",
        key_id: "key_1",
        scopes: ["assets:read"],
      }),
    );
    const kadr = client(fetch);
    const presets = await kadr.presets.list();
    expect(presets).toHaveLength(1);
    // params are passed through verbatim
    expect(presets[0]!.params).toEqual({ kind: "image", limits: { maxInputBytes: 1000, snake_key: 1 } });
    expect((await kadr.presets.get("photo-lite")).createdAt).toBe("2026-01-01T00:00:00Z");
    const usage = await kadr.namespaces.usage("org_8f2c");
    expect(usage).toEqual({
      namespace: "org_8f2c",
      usedBytes: 10,
      reservedBytes: 5,
      assetCount: 2,
      videoSecMonth: 90,
      quotaBytes: 1000,
      quotaAssets: null,
      quotaVideoSecMonth: null,
    });
    expect((await kadr.namespaces.setQuota("org_8f2c", { quotaBytes: 1000 })).quotaBytes).toBe(1000);
    expect(await kadr.ping()).toEqual({
      ok: true,
      project: "acme",
      projectId: "prj_1",
      environment: "test",
      keyId: "key_1",
      scopes: ["assets:read"],
    });
    expect(calls.map((c) => `${c.init.method} ${c.url}`)).toEqual([
      "GET http://kadr.internal:8080/v1/presets",
      "GET http://kadr.internal:8080/v1/presets/photo-lite",
      "GET http://kadr.internal:8080/v1/namespaces/org_8f2c/usage",
      "PUT http://kadr.internal:8080/v1/namespaces/org_8f2c/quota",
      "GET http://kadr.internal:8080/v1/ping",
    ]);
    expect(JSON.parse(calls[3]!.init.body as string)).toEqual({ quota_bytes: 1000 });
  });
});
