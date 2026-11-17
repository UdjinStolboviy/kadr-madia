import { afterEach, describe, expect, it, vi } from "vitest";
import {
  Kadr,
  KadrError,
  KadrNetworkError,
  KadrTimeoutError,
  type FetchLike,
  type KadrOptions,
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

const wireAsset = (id: string) => ({
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
  it("returns a multipart ticket", async () => {
    const { fetch } = mockFetch(
      json(
        {
          upload_id: "upl_2",
          method: "MULTIPART",
          part_size: 16777216,
          parts: [
            { number: 1, url: "https://storage.example.com/p1" },
            { number: 2, url: "https://storage.example.com/p2" },
          ],
          expires_at: "2026-11-16T21:15:00Z",
        },
        201,
      ),
    );
    const res = await client(fetch).uploads.create({
      preset: "video-720p",
      namespace: "org_8f2c",
      visibility: "public",
      contentType: "video/mp4",
      size: 100 * 1024 * 1024,
    });
    expect(res.kind).toBe("upload");
    if (res.kind === "upload" && res.method === "MULTIPART") {
      expect(res.partSize).toBe(16777216);
      expect(res.parts).toHaveLength(2);
    } else {
      throw new Error("expected a multipart ticket");
    }
  });

  it("returns a deduplicated asset on 200 (bare or wrapped)", async () => {
    const { fetch } = mockFetch(json(wireAsset("ast_d1")), json({ asset: wireAsset("ast_d2") }));
    const kadr = client(fetch);
    const input = {
      preset: "photo-lite",
      namespace: "org_8f2c",
      visibility: "private" as const,
      contentType: "image/jpeg",
      size: 1,
      sha256: "ab",
    };
    const a = await kadr.uploads.create(input);
    const b = await kadr.uploads.create(input);
    expect(a.kind === "asset" && a.asset.id).toBe("ast_d1");
    expect(b.kind === "asset" && b.asset.id).toBe("ast_d2");
  });

  it("completes with parts and wait, requests parts and aborts", async () => {
    const { fetch, calls } = mockFetch(
      json(wireAsset("ast_1")),
      json({ parts: [{ number: 21, url: "https://storage.example.com/p21" }] }),
      new Response(null, { status: 204 }),
    );
    const kadr = client(fetch);
    const asset = await kadr.uploads.complete("upl_1", {
      parts: [{ number: 1, etag: '"abc"' }],
      wait: "5s",
    });
    expect(asset.id).toBe("ast_1");
    expect(calls[0]!.url).toBe("http://kadr.internal:8080/v1/uploads/upl_1/complete?wait=5s");
    expect(JSON.parse(calls[0]!.init.body as string)).toEqual({ parts: [{ number: 1, etag: '"abc"' }] });

    const parts = await kadr.uploads.parts("upl_1", [21]);
    expect(parts).toEqual([{ number: 21, url: "https://storage.example.com/p21" }]);
    expect(JSON.parse(calls[1]!.init.body as string)).toEqual({ part_numbers: [21] });

    await kadr.uploads.abort("upl_1");
    expect(calls[2]!.init.method).toBe("DELETE");
    expect(calls[2]!.url).toBe("http://kadr.internal:8080/v1/uploads/upl_1");
  });
});

describe("pagination", () => {
  it("iterates across 3 pages", async () => {
    const { fetch, calls } = mockFetch(
      json({ data: [wireAsset("ast_1"), wireAsset("ast_2")], next_cursor: "c2" }),
      json({ data: [wireAsset("ast_3")], next_cursor: "c3" }),
      json({ data: [wireAsset("ast_4")], next_cursor: null }),
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
    expect(page.data.map((a) => a.id)).toEqual(["ast_1"]);
    expect(page.nextCursor).toBe("c2");
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
          errors: [{ field: "size", message: "too big" }],
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
    expect(e.errors).toEqual([{ field: "size", message: "too big" }]);
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
    await expect(client(fetch).assets.reprocess("ast_1")).rejects.toMatchObject({ status: 503 });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("retries POST with an idempotency key", async () => {
    const { fetch, calls } = mockFetch(problem({ code: "unavailable" }, 503), json(wireAsset("ast_1")));
    await client(fetch).assets.reprocess("ast_1", { preset: "photo-lite" }, { idempotencyKey: "k1" });
    expect(fetch).toHaveBeenCalledTimes(2);
    for (const c of calls) {
      expect((c.init.headers as Record<string, string>)["Idempotency-Key"]).toBe("k1");
    }
  });

  it("does not retry PATCH, PUT or DELETE", async () => {
    const { fetch } = mockFetch(
      problem({ code: "unavailable" }, 503),
      problem({ code: "unavailable" }, 503),
      new TypeError("fetch failed"),
    );
    const kadr = client(fetch);
    await expect(kadr.assets.update("ast_1", { visibility: "public" })).rejects.toMatchObject({ status: 503 });
    await expect(kadr.namespaces.setQuota("org_8f2c", { maxBytes: 1 })).rejects.toMatchObject({ status: 503 });
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
      filename: "scan.pdf",
      contentType: "application/pdf",
      metadata: { entity: "receipt", entityId: "rcp_1" },
    });
    const { url, init } = calls[0]!;
    expect(url).toBe("http://kadr.internal:8080/v1/assets");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
    const form = init.body as FormData;
    expect(form).toBeInstanceOf(FormData);
    expect(form.get("preset")).toBe("doc-lite");
    expect(form.get("namespace")).toBe("org_8f2c");
    expect(form.get("visibility")).toBe("private");
    expect(form.get("filename")).toBe("scan.pdf");
    expect(form.get("content_type")).toBe("application/pdf");
    expect(JSON.parse(form.get("metadata") as string)).toEqual({ entity: "receipt", entityId: "rcp_1" });
    const file = form.get("file") as File;
    expect(file.name).toBe("scan.pdf");
    expect(file.type).toBe("application/pdf");
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

describe("presets and namespaces", () => {
  it("calls the right endpoints", async () => {
    const { fetch, calls } = mockFetch(
      json({ data: [{ slug: "photo-lite", version: 3 }] }),
      json({ slug: "photo-lite", version: 3, created_at: "2026-01-01T00:00:00Z" }),
      json({ namespace: "org_8f2c", bytes: 10, count: 2, video_minutes_this_month: 1.5 }),
      json({ max_bytes: 1000 }),
      new Response(null, { status: 202 }),
    );
    const kadr = client(fetch);
    expect(await kadr.presets.list()).toEqual([{ slug: "photo-lite", version: 3 }]);
    expect((await kadr.presets.get("photo-lite")).createdAt).toBe("2026-01-01T00:00:00Z");
    expect((await kadr.namespaces.usage("org_8f2c")).videoMinutesThisMonth).toBe(1.5);
    expect(await kadr.namespaces.setQuota("org_8f2c", { maxBytes: 1000 })).toEqual({ maxBytes: 1000 });
    await kadr.namespaces.purge("org_8f2c");
    expect(calls.map((c) => `${c.init.method} ${c.url}`)).toEqual([
      "GET http://kadr.internal:8080/v1/presets",
      "GET http://kadr.internal:8080/v1/presets/photo-lite",
      "GET http://kadr.internal:8080/v1/namespaces/org_8f2c/usage",
      "PUT http://kadr.internal:8080/v1/namespaces/org_8f2c/quota",
      "DELETE http://kadr.internal:8080/v1/namespaces/org_8f2c",
    ]);
    expect(JSON.parse(calls[3]!.init.body as string)).toEqual({ max_bytes: 1000 });
  });
});
