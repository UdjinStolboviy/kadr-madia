import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { createHmac, randomBytes } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { signUrl } from "../../../../sdk/node/src/signing.ts";

// Real workerd via `wrangler dev --local` with a local (miniflare) R2 bucket.
const ROOT = new URL("../..", import.meta.url).pathname;
const CONFIG = "wrangler.dev.toml";
const SECRET = "test-secret-0123456789abcdef";
const KID = "k3";
const FAR = 4102444800; // 2100-01-01

const SMALL_KEY = "acme/blb_01JDTEST/small.txt";
const SMALL = Buffer.from("0123456789abcdefghijklmnopqrstuvwxyz".repeat(10)); // 360 bytes
const LARGE_KEY = "acme/blb_01JDTEST/large.bin";
const LARGE = randomBytes(6 * 1024 * 1024); // > CACHE_MAX_BYTES (5 MiB): never cached
const AVIF_KEY = "acme/blb_01JDTEST/main.avif";
const PROBE_KEY = "acme/blb_01JDTEST/probe.txt";

let tmp: string;
let proc: ChildProcess;
let base: string;
let log = "";

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address() as { port: number };
      s.close(() => resolve(port));
    });
  });
}

function wrangler(args: string[]) {
  const r = spawnSync("pnpm", ["exec", "wrangler", ...args], { cwd: ROOT, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`wrangler ${args.join(" ")} failed:\n${r.stdout}\n${r.stderr}`);
}

function put(key: string, data: Buffer, contentType: string) {
  const f = join(tmp, `obj-${Math.random().toString(36).slice(2)}`);
  writeFileSync(f, data);
  wrangler(["r2", "object", "put", `kadr-private/${key}`, "--file", f, "--content-type", contentType, "--local", "--persist-to", join(tmp, "state"), "-c", CONFIG]);
}

/** Independent re-implementation of the documented canonical form (not the SDK). */
function sign(key: string, exp: number, download = "", secret = SECRET): string {
  return createHmac("sha256", Buffer.from(secret, "utf8")).update(`GET\n/${key}\n${exp}\n${download}`, "utf8").digest("base64url");
}

function url(key: string, o: { exp?: number; kid?: string; sig?: string; download?: string } = {}): string {
  const exp = o.exp ?? FAR;
  const dl = o.download ?? "";
  const q = new URLSearchParams({ exp: String(exp), kid: o.kid ?? KID, sig: o.sig ?? sign(key, exp, dl) });
  if (o.download !== undefined) q.set("download", o.download);
  return `${base}/${key.split("/").map(encodeURIComponent).join("/")}?${q}`;
}

beforeAll(async () => {
  tmp = mkdtempSync(join(tmpdir(), "kadr-edge-it-"));
  put(SMALL_KEY, SMALL, "text/plain");
  put(LARGE_KEY, LARGE, "application/octet-stream");
  put(AVIF_KEY, Buffer.from("avif-bytes"), "image/avif");
  put(PROBE_KEY, Buffer.from("version-1"), "text/plain");

  const port = await freePort();
  base = `http://127.0.0.1:${port}`;
  proc = spawn(
    "pnpm",
    ["exec", "wrangler", "dev", "-c", CONFIG, "--local", "--persist-to", join(tmp, "state"), "--port", String(port), "--ip", "127.0.0.1", "--var", `SIGNING_KEYS:${JSON.stringify({ [KID]: SECRET })}`],
    { cwd: ROOT, env: { ...process.env, CI: "1", WRANGLER_SEND_METRICS: "false", NO_COLOR: "1" } },
  );
  await new Promise<void>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`wrangler dev not ready:\n${log}`)), 90_000);
    const on = (d: Buffer) => {
      log += d.toString();
      if (/Ready on/.test(log)) {
        clearTimeout(t);
        resolve();
      }
    };
    proc.stdout?.on("data", on);
    proc.stderr?.on("data", on);
    proc.once("exit", (c) => reject(new Error(`wrangler exited ${c}:\n${log}`)));
  });
});

afterAll(async () => {
  if (proc && proc.exitCode === null) {
    proc.removeAllListeners("exit");
    proc.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 500));
    if (proc.exitCode === null) proc.kill("SIGKILL");
  }
  if (tmp) rmSync(tmp, { recursive: true, force: true });
});

describe("workerd: signed delivery", () => {
  it("valid signed URL -> 200 with exact bytes and headers", async () => {
    const r = await fetch(url(SMALL_KEY));
    expect(r.status).toBe(200);
    expect(Buffer.from(await r.arrayBuffer()).equals(SMALL)).toBe(true);
    expect(r.headers.get("content-type")).toBe("text/plain");
    expect(r.headers.get("accept-ranges")).toBe("bytes");
    expect(r.headers.get("etag")).toBeTruthy();
    expect(r.headers.get("cache-control")).toMatch(/^private, max-age=\d+$/);
    expect(r.headers.get("x-content-type-options")).toBe("nosniff");
  });

  it("URL produced by the Node SDK is accepted", async () => {
    const u = signUrl({ kid: KID, secret: SECRET, baseUrl: base }, SMALL_KEY, { ttl: 600, download: "a b.txt" });
    const r = await fetch(u);
    expect(r.status).toBe(200);
    expect(r.headers.get("content-disposition")).toContain('filename="a b.txt"');
  });

  it("Go-compatible signature (cross-check vector + far-future exp) is accepted", async () => {
    // internal/pipeline/signing_test.go vector: Go and Node agree on this exact value.
    expect(sign(AVIF_KEY, 1763330400, "receipt.avif")).toBe("6htVHR_ussyeRR5QQBVvsmobaFla_5qrDeHoDtYVw5M");
    expect(sign(AVIF_KEY, 1763330400)).toBe("O15QXzAjaI3KPNfA_FguxK3zQPZD0s-YMgTTH8SDHNU");
    // The vector's own exp is in the past, so the Worker must verify it (HMAC ok) and then reject as expired.
    const expired = await fetch(url(AVIF_KEY, { exp: 1763330400 }));
    expect(expired.status).toBe(403);
    // Same algorithm, far-future exp: accepted by the Worker.
    const ok = await fetch(url(AVIF_KEY));
    expect(ok.status).toBe(200);
    expect(await ok.text()).toBe("avif-bytes");
    const dl = await fetch(url(AVIF_KEY, { download: "receipt.avif" }));
    expect(dl.status).toBe(200);
  });

  it("tampered sig, wrong secret, wrong key, wrong download -> 403", async () => {
    const good = sign(SMALL_KEY, FAR);
    const bad = (good[0] === "A" ? "B" : "A") + good.slice(1);
    expect((await fetch(url(SMALL_KEY, { sig: bad }))).status).toBe(403);
    expect((await fetch(url(SMALL_KEY, { sig: sign(SMALL_KEY, FAR, "", "other-secret") }))).status).toBe(403);
    expect((await fetch(url(SMALL_KEY, { sig: sign(AVIF_KEY, FAR) }))).status).toBe(403);
    // download is covered by the signature
    expect((await fetch(url(SMALL_KEY, { sig: good, download: "evil.txt" }))).status).toBe(403);
    expect((await fetch(url(SMALL_KEY, { sig: "not-base64!" }))).status).toBe(403);
    expect((await fetch(`${base}/${SMALL_KEY}`)).status).toBe(403);
  });

  it("expired -> 403 (validly signed)", async () => {
    expect((await fetch(url(SMALL_KEY, { exp: Math.floor(Date.now() / 1000) - 10 }))).status).toBe(403);
  });

  it("unknown kid -> 403", async () => {
    expect((await fetch(url(SMALL_KEY, { kid: "nope" }))).status).toBe(403);
  });

  it("path traversal -> 403", async () => {
    const r = await fetch(`${base}/acme/../acme/blb_01JDTEST/small.txt?exp=${FAR}&kid=${KID}&sig=${sign("acme/blb_01JDTEST/small.txt", FAR)}`);
    // fetch normalizes dot segments; the normalized path is validly signed.
    expect([200, 403]).toContain(r.status);
    const raw = await fetch(`${base}/acme/%2e%2e/acme/blb_01JDTEST/small.txt?exp=${FAR}&kid=${KID}&sig=${sign("acme/../acme/blb_01JDTEST/small.txt", FAR)}`);
    expect(raw.status).toBe(403);
  });

  it("missing object (validly signed) -> 404", async () => {
    const key = "acme/blb_01JDTEST/missing.bin";
    expect((await fetch(url(key))).status).toBe(404);
    expect((await fetch(url(key), { method: "HEAD" })).status).toBe(404);
    expect((await fetch(url(key), { headers: { Range: "bytes=0-9" } })).status).toBe(404);
  });

  it("POST/PUT/DELETE -> 405 with Allow", async () => {
    for (const method of ["POST", "PUT", "DELETE"]) {
      const r = await fetch(url(SMALL_KEY), { method, body: method === "DELETE" ? undefined : "x" });
      expect(r.status).toBe(405);
      expect(r.headers.get("allow")).toBe("GET, HEAD");
    }
  });
});

describe("workerd: ranges, conditionals, HEAD", () => {
  it("Range 0-99 -> 206 + Content-Range", async () => {
    const r = await fetch(url(SMALL_KEY), { headers: { Range: "bytes=0-99" } });
    expect(r.status).toBe(206);
    expect(r.headers.get("content-range")).toBe(`bytes 0-99/${SMALL.length}`);
    expect(r.headers.get("content-length")).toBe("100");
    expect(Buffer.from(await r.arrayBuffer()).equals(SMALL.subarray(0, 100))).toBe(true);
  });

  it("suffix range and open-ended range", async () => {
    const s = await fetch(url(SMALL_KEY), { headers: { Range: "bytes=-50" } });
    expect(s.status).toBe(206);
    expect(s.headers.get("content-range")).toBe(`bytes ${SMALL.length - 50}-${SMALL.length - 1}/${SMALL.length}`);
    expect(Buffer.from(await s.arrayBuffer()).equals(SMALL.subarray(SMALL.length - 50))).toBe(true);

    const o = await fetch(url(SMALL_KEY), { headers: { Range: "bytes=300-" } });
    expect(o.status).toBe(206);
    expect(o.headers.get("content-range")).toBe(`bytes 300-${SMALL.length - 1}/${SMALL.length}`);
    expect(Buffer.from(await o.arrayBuffer()).equals(SMALL.subarray(300))).toBe(true);
  });

  it("unsatisfiable range -> 416", async () => {
    const r = await fetch(url(SMALL_KEY), { headers: { Range: "bytes=9999-" } });
    expect(r.status).toBe(416);
    expect(r.headers.get("content-range")).toBe(`bytes */${SMALL.length}`);
  });

  it("If-None-Match -> 304 (strong, list, *)", async () => {
    const etag = (await fetch(url(SMALL_KEY), { method: "HEAD" })).headers.get("etag") as string;
    expect(etag).toBeTruthy();
    for (const inm of [etag, `"zzz", ${etag}`, "*"]) {
      const r = await fetch(url(SMALL_KEY), { headers: { "If-None-Match": inm } });
      expect(r.status).toBe(304);
      expect((await r.arrayBuffer()).byteLength).toBe(0);
    }
    expect((await fetch(url(SMALL_KEY), { headers: { "If-None-Match": '"other"' } })).status).toBe(200);
  });

  it("download -> Content-Disposition (ASCII fallback + RFC 5987)", async () => {
    const r = await fetch(url(SMALL_KEY, { download: "résumé final.txt" }));
    expect(r.status).toBe(200);
    const cd = r.headers.get("content-disposition") ?? "";
    expect(cd).toContain("attachment;");
    expect(cd).toContain("filename*=UTF-8''r%C3%A9sum%C3%A9%20final.txt");
    await r.arrayBuffer();
    // No download param -> no Content-Disposition.
    expect((await fetch(url(SMALL_KEY))).headers.get("content-disposition")).toBeNull();
  });

  it("HEAD -> 200, headers, empty body", async () => {
    const r = await fetch(url(SMALL_KEY), { method: "HEAD" });
    expect(r.status).toBe(200);
    expect(r.headers.get("content-length")).toBe(String(SMALL.length));
    expect(r.headers.get("content-type")).toBe("text/plain");
    expect(r.headers.get("etag")).toBeTruthy();
    expect((await r.arrayBuffer()).byteLength).toBe(0);
  });
});

describe("workerd: large object (not cached path)", () => {
  it("full 6 MiB GET streams exact bytes", async () => {
    const r = await fetch(url(LARGE_KEY));
    expect(r.status).toBe(200);
    expect(r.headers.get("content-length")).toBe(String(LARGE.length));
    expect(Buffer.from(await r.arrayBuffer()).equals(LARGE)).toBe(true);
  });

  it("ranges on the large object hit the right bytes (also across the 5 MiB mark)", async () => {
    const a = 5 * 1024 * 1024 - 10;
    const r = await fetch(url(LARGE_KEY), { headers: { Range: `bytes=${a}-${a + 99}` } });
    expect(r.status).toBe(206);
    expect(r.headers.get("content-range")).toBe(`bytes ${a}-${a + 99}/${LARGE.length}`);
    expect(Buffer.from(await r.arrayBuffer()).equals(LARGE.subarray(a, a + 100))).toBe(true);
    const tail = await fetch(url(LARGE_KEY), { headers: { Range: "bytes=-1000" } });
    expect(Buffer.from(await tail.arrayBuffer()).equals(LARGE.subarray(LARGE.length - 1000))).toBe(true);
  });

  it("repeat request is consistent", async () => {
    const r = await fetch(url(LARGE_KEY));
    expect(Buffer.from(await r.arrayBuffer()).equals(LARGE)).toBe(true);
  });
});

describe("workerd: Cache API", () => {
  it("second request is served consistently (hit or miss), incl. range/304 from the cached copy", async () => {
    const first = await fetch(url(SMALL_KEY));
    const b1 = Buffer.from(await first.arrayBuffer());
    await new Promise((r) => setTimeout(r, 300)); // let ctx.waitUntil(cache.put) settle
    const second = await fetch(url(SMALL_KEY));
    const b2 = Buffer.from(await second.arrayBuffer());
    expect(b2.equals(b1)).toBe(true);
    expect(second.headers.get("etag")).toBe(first.headers.get("etag"));
    expect(second.headers.get("content-length")).toBe(first.headers.get("content-length"));
    const rng = await fetch(url(SMALL_KEY), { headers: { Range: "bytes=10-19" } });
    expect(rng.status).toBe(206);
    expect(Buffer.from(await rng.arrayBuffer()).equals(SMALL.subarray(10, 20))).toBe(true);
  });

  it("cache signature isolation: a bad sig is never served from a cached copy", async () => {
    await (await fetch(url(SMALL_KEY))).arrayBuffer();
    await new Promise((r) => setTimeout(r, 300));
    expect((await fetch(url(SMALL_KEY, { sig: sign(SMALL_KEY, FAR, "", "other") }))).status).toBe(403);
  });

  it("observation: does the local Cache API actually cache? (informational)", async () => {
    const first = await (await fetch(url(PROBE_KEY))).text();
    await new Promise((r) => setTimeout(r, 300));
    put(PROBE_KEY, Buffer.from("version-2"), "text/plain"); // overwrite behind the Worker's back
    const second = await (await fetch(url(PROBE_KEY))).text();
    const cached = second === first;
    process.stderr.write(`\n[cache-probe] first=${first} second=${second} => local Cache API ${cached ? "IS caching" : "is a no-op / not populated"}`);
    expect(["version-1", "version-2"]).toContain(second);
  });
});
