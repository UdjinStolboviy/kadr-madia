import { describe, expect, it } from "vitest";
import { signUrl } from "../../../sdk/node/src/signing.ts";
import { handle } from "../src/index.ts";
import type { CacheLike, Env, R2ObjectBodyLike, R2ObjectLike } from "../src/types.ts";

const secret = "test-secret-0123456789abcdef";
const signing = { kid: "k3", secret, baseUrl: "https://media.example.com" };
const NOW = 1763326800_000;
const KEY = "acme/blb_01/video.mp4";
const DATA = new TextEncoder().encode("0123456789abcdefghij"); // 20 bytes

function makeEnv() {
  const calls: string[] = [];
  const meta = (): R2ObjectLike => ({
    key: KEY,
    size: DATA.length,
    httpEtag: '"abc"',
    uploaded: new Date("2025-01-01T00:00:00Z"),
    httpMetadata: { contentType: "video/mp4" },
  });
  const bucket = {
    async head(k: string) {
      calls.push("head");
      return k === KEY ? meta() : null;
    },
    async get(k: string, o?: { range?: { offset: number; length: number } }): Promise<R2ObjectBodyLike | null> {
      calls.push(o?.range ? `get:${o.range.offset}+${o.range.length}` : "get");
      if (k !== KEY) return null;
      const part = o?.range ? DATA.slice(o.range.offset, o.range.offset + o.range.length) : DATA;
      return { ...meta(), body: new Response(part).body as ReadableStream<Uint8Array> };
    },
  };
  const env: Env = { PRIVATE: bucket, SIGNING_KEYS: JSON.stringify({ k3: secret }) };
  return { env, calls };
}

const ctx = () => {
  const pending: Promise<unknown>[] = [];
  return { waitUntil: (p: Promise<unknown>) => void pending.push(p), pending };
};
const signed = (extra: { download?: string; ttl?: number } = {}) =>
  signUrl(signing, KEY, { ttl: extra.ttl ?? 3600, now: NOW, ...(extra.download ? { download: extra.download } : {}) });
const req = (url: string, init?: RequestInit) => new Request(url, init);

describe("worker", () => {
  it("200 full object with private cache headers", async () => {
    const { env } = makeEnv();
    const res = await handle(req(signed()), env, ctx(), { now: NOW });
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("0123456789abcdefghij");
    expect(res.headers.get("Accept-Ranges")).toBe("bytes");
    expect(res.headers.get("Cache-Control")).toBe("private, max-age=3600");
    expect(res.headers.get("ETag")).toBe('"abc"');
    expect(res.headers.get("Content-Type")).toBe("video/mp4");
    expect(res.headers.get("Content-Disposition")).toBeNull();
  });

  it("max-age is capped by remaining validity", async () => {
    const { env } = makeEnv();
    const res = await handle(req(signed({ ttl: 60 })), env, ctx(), { now: NOW });
    expect(res.headers.get("Cache-Control")).toBe("private, max-age=60");
  });

  it("Content-Disposition from download", async () => {
    const { env } = makeEnv();
    const res = await handle(req(signed({ download: 'a "b"é.mp4' })), env, ctx(), { now: NOW });
    expect(res.headers.get("Content-Disposition")).toBe(
      `attachment; filename="a _b__.mp4"; filename*=UTF-8''a%20%22b%22%C3%A9.mp4`,
    );
  });

  it("403 on bad, missing and expired signature", async () => {
    const { env } = makeEnv();
    const u = signed();
    expect((await handle(req(u.replace("main", "x").replace("video", "vid")), env, ctx(), { now: NOW })).status).toBe(403);
    expect((await handle(req(u.replace(/&sig=[^&]+/, "")), env, ctx(), { now: NOW })).status).toBe(403);
    expect((await handle(req(u), env, ctx(), { now: NOW + 3601_000 })).status).toBe(403);
    expect((await handle(req("https://media.example.com/"), env, ctx(), { now: NOW })).status).toBe(403);
  });

  it("404 for a missing object with a valid signature", async () => {
    const { env } = makeEnv();
    const u = signUrl(signing, "acme/nope.mp4", { ttl: 60, now: NOW });
    expect((await handle(req(u), env, ctx(), { now: NOW })).status).toBe(404);
  });

  it("405 for non-GET/HEAD, without touching the bucket", async () => {
    const { env, calls } = makeEnv();
    for (const m of ["POST", "PUT", "DELETE", "PATCH"]) {
      const res = await handle(req(signed(), { method: m, body: m === "POST" || m === "PUT" ? "x" : null }), env, ctx(), { now: NOW });
      expect(res.status).toBe(405);
      expect(res.headers.get("Allow")).toBe("GET, HEAD");
    }
    expect(calls).toEqual([]);
  });

  it("no listing: bucket root / prefixes without a valid signature are 403", async () => {
    const { env, calls } = makeEnv();
    expect((await handle(req("https://media.example.com/?list=1&prefix=acme/"), env, ctx(), { now: NOW })).status).toBe(403);
    expect(calls).toEqual([]);
  });

  it("206 for Range", async () => {
    const { env, calls } = makeEnv();
    const res = await handle(req(signed(), { headers: { Range: "bytes=5-9" } }), env, ctx(), { now: NOW });
    expect(res.status).toBe(206);
    expect(res.headers.get("Content-Range")).toBe("bytes 5-9/20");
    expect(res.headers.get("Content-Length")).toBe("5");
    expect(await res.text()).toBe("56789");
    expect(calls).toEqual(["head", "get:5+5"]);
  });

  it("open-ended and suffix ranges", async () => {
    const { env } = makeEnv();
    const a = await handle(req(signed(), { headers: { Range: "bytes=15-" } }), env, ctx(), { now: NOW });
    expect(a.headers.get("Content-Range")).toBe("bytes 15-19/20");
    const b = await handle(req(signed(), { headers: { Range: "bytes=-4" } }), env, ctx(), { now: NOW });
    expect(b.headers.get("Content-Range")).toBe("bytes 16-19/20");
    expect(await b.text()).toBe("ghij");
  });

  it("416 for unsatisfiable range", async () => {
    const { env } = makeEnv();
    const res = await handle(req(signed(), { headers: { Range: "bytes=50-60" } }), env, ctx(), { now: NOW });
    expect(res.status).toBe(416);
    expect(res.headers.get("Content-Range")).toBe("bytes */20");
  });

  it("If-Range mismatch yields full 200", async () => {
    const { env } = makeEnv();
    const res = await handle(req(signed(), { headers: { Range: "bytes=0-1", "If-Range": '"other"' } }), env, ctx(), { now: NOW });
    expect(res.status).toBe(200);
    expect((await res.arrayBuffer()).byteLength).toBe(20);
  });

  it("304 on If-None-Match without reading the body", async () => {
    const { env, calls } = makeEnv();
    const res = await handle(req(signed(), { headers: { "If-None-Match": '"abc"' } }), env, ctx(), { now: NOW });
    expect(res.status).toBe(304);
    expect(res.headers.get("ETag")).toBe('"abc"');
    expect(calls).toEqual(["head"]);
    const miss = await handle(req(signed(), { headers: { "If-None-Match": '"zzz"' } }), env, ctx(), { now: NOW });
    expect(miss.status).toBe(200);
  });

  it("HEAD returns headers only", async () => {
    const { env } = makeEnv();
    const res = await handle(req(signed(), { method: "HEAD" }), env, ctx(), { now: NOW });
    expect(res.status).toBe(200);
    expect(res.headers.get("Content-Length")).toBe("20");
    expect(await res.text()).toBe("");
  });

  describe("cache", () => {
    const makeCache = () => {
      const store = new Map<string, Response>();
      const keys: string[] = [];
      const cache: CacheLike = {
        async match(r) {
          return store.get(r.url)?.clone();
        },
        async put(r, res) {
          keys.push(r.url);
          store.set(r.url, res);
        },
      };
      return { cache, keys };
    };

    it("stores under a key without signature query; hits skip R2 and keep per-request headers", async () => {
      const { env, calls } = makeEnv();
      const { cache, keys } = makeCache();
      const c = ctx();
      const r1 = await handle(req(signed()), env, c, { now: NOW, cache });
      await r1.arrayBuffer();
      await Promise.all(c.pending);
      expect(keys).toEqual([`https://media.example.com/${KEY}`]);

      calls.length = 0;
      const r2 = await handle(req(signed({ download: "x.mp4" })), env, ctx(), { now: NOW, cache });
      expect(calls).toEqual([]);
      expect(r2.status).toBe(200);
      expect(r2.headers.get("Cache-Control")).toMatch(/^private/);
      expect(r2.headers.get("Content-Disposition")).toContain("x.mp4");
      expect(await r2.text()).toBe("0123456789abcdefghij");

      const r3 = await handle(req(signed(), { headers: { Range: "bytes=0-3" } }), env, ctx(), { now: NOW, cache });
      expect(r3.status).toBe(206);
      expect(await r3.text()).toBe("0123");
      const r4 = await handle(req(signed(), { headers: { "If-None-Match": '"abc"' } }), env, ctx(), { now: NOW, cache });
      expect(r4.status).toBe(304);
      expect(calls).toEqual([]);
    });

    it("never serves a cache hit to an invalid signature", async () => {
      const { env } = makeEnv();
      const { cache } = makeCache();
      const c = ctx();
      await (await handle(req(signed()), env, c, { now: NOW, cache })).arrayBuffer();
      await Promise.all(c.pending);
      const bad = signed().replace(/sig=[^&]+/, "sig=" + "A".repeat(43));
      expect((await handle(req(bad), env, ctx(), { now: NOW, cache })).status).toBe(403);
    });
  });
});
