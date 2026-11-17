import { describe, expect, it, vi } from "vitest";
import {
  KadrAbortError,
  KadrUploadError,
  backoffDelay,
  fitWithin,
  preResizeImage,
  putMultipart,
  resizeSupported,
  uploadFile,
} from "../src/index.js";
import type { CompletedPart, MultipartUploadTicket, PutUploadTicket } from "../src/index.js";
import { fakeXhrFactory, noSleep, readText } from "./helpers.js";

const putTicket: PutUploadTicket = {
  upload_id: "upl_1",
  method: "PUT",
  url: "https://storage.example/put",
  headers: { "Content-Type": "image/jpeg", "Content-Length": "5" },
};

describe("uploadFile (PUT)", () => {
  it("uploads with ticket headers (minus Content-Length), reports progress and completes", async () => {
    const xhr = fakeXhrFactory(() => ({ status: 200 }));
    const file = new File(["hello"], "a.jpg", { type: "image/jpeg" });
    const createTicket = vi.fn(() => Promise.resolve(putTicket));
    const complete = vi.fn((id: string, parts?: CompletedPart[]) => Promise.resolve({ id, parts }));
    const progress: number[] = [];
    const asset = await uploadFile(file, {
      createTicket,
      complete,
      createXhr: xhr.create,
      onProgress: (p) => progress.push(p.fraction),
    });
    expect(createTicket).toHaveBeenCalledWith({ filename: "a.jpg", content_type: "image/jpeg", size: 5 });
    expect(xhr.requests).toHaveLength(1);
    expect(xhr.requests[0]!.headers).toEqual({ "Content-Type": "image/jpeg" });
    expect(complete).toHaveBeenCalledWith("upl_1", undefined);
    expect(asset).toEqual({ id: "upl_1", parts: undefined });
    expect(progress).toContain(0.5);
    expect(progress.at(-1)).toBe(1);
  });

  it("retries 5xx and network errors, then succeeds", async () => {
    const responses = [{ status: 503 }, { network: true }, { status: 200 }];
    const xhr = fakeXhrFactory(() => responses.shift()!);
    await uploadFile(new Blob(["x"], { type: "image/png" }), {
      createTicket: () => Promise.resolve(putTicket),
      complete: () => Promise.resolve(1),
      createXhr: xhr.create,
      sleep: noSleep,
    });
    expect(xhr.requests).toHaveLength(3);
  });

  it("does not retry 403 and gives up after the retry budget", async () => {
    const x403 = fakeXhrFactory(() => ({ status: 403 }));
    const opts = { createTicket: () => Promise.resolve(putTicket), complete: () => Promise.resolve(1), sleep: noSleep };
    await expect(uploadFile(new Blob(["x"]), { ...opts, createXhr: x403.create })).rejects.toMatchObject({
      status: 403,
      retryable: false,
    });
    expect(x403.requests).toHaveLength(1);

    const x500 = fakeXhrFactory(() => ({ status: 500 }));
    await expect(uploadFile(new Blob(["x"]), { ...opts, retries: 2, createXhr: x500.create })).rejects.toBeInstanceOf(
      KadrUploadError,
    );
    expect(x500.requests).toHaveLength(3);
  });

  it("aborts before upload starts", async () => {
    const ctl = new AbortController();
    ctl.abort();
    const createTicket = vi.fn();
    await expect(
      uploadFile(new Blob(["x"]), { createTicket, complete: vi.fn(), signal: ctl.signal }),
    ).rejects.toBeInstanceOf(KadrAbortError);
    expect(createTicket).not.toHaveBeenCalled();
  });

  it("aborts an in-flight request", async () => {
    const ctl = new AbortController();
    const xhr = fakeXhrFactory(() => new Promise(() => undefined));
    const p = uploadFile(new Blob(["x"]), {
      createTicket: () => Promise.resolve(putTicket),
      complete: () => Promise.resolve(1),
      createXhr: xhr.create,
      signal: ctl.signal,
    });
    await new Promise((r) => setTimeout(r, 5));
    ctl.abort();
    await expect(p).rejects.toMatchObject({ name: "AbortError" });
  });
});

describe("multipart", () => {
  const ticket: MultipartUploadTicket = {
    upload_id: "upl_m",
    method: "MULTIPART",
    part_size: 4,
    parts: [1, 2, 3].map((n) => ({ part_number: n, url: `https://s/p${n}` })),
  };

  it("slices parts, runs in parallel up to the limit and returns sorted ETags", async () => {
    const seen: Record<string, string> = {};
    const xhr = fakeXhrFactory(async (r) => {
      seen[r.url] = await readText(r.body);
      await new Promise((res) => setTimeout(res, 5));
      return { etag: `"e-${r.url.slice(-1)}"` };
    });
    const complete = vi.fn((_id: string, parts?: CompletedPart[]) => Promise.resolve(parts));
    const parts = await uploadFile(new Blob(["aaaabbbbcc"]), {
      createTicket: () => Promise.resolve(ticket),
      complete,
      createXhr: xhr.create,
      concurrency: 2,
    });
    expect(seen).toEqual({ "https://s/p1": "aaaa", "https://s/p2": "bbbb", "https://s/p3": "cc" });
    expect(xhr.maxActive()).toBe(2);
    expect(parts).toEqual([
      { part_number: 1, etag: '"e-1"' },
      { part_number: 2, etag: '"e-2"' },
      { part_number: 3, etag: '"e-3"' },
    ]);
    expect(JSON.parse(JSON.stringify({ parts }))).toEqual({ parts });
  });

  it("retries a single part without restarting the others", async () => {
    let failed = false;
    const xhr = fakeXhrFactory((r) => {
      if (r.url.endsWith("p2") && !failed) {
        failed = true;
        return { status: 500 };
      }
      return { etag: "x" };
    });
    await putMultipart(new Blob(["aaaabbbbcc"]), ticket, { createXhr: xhr.create, sleep: noSleep });
    expect(xhr.requests.map((r) => r.url).filter((u) => u.endsWith("p2"))).toHaveLength(2);
    expect(xhr.requests).toHaveLength(4);
  });

  it("fails on a missing ETag and on a part-count mismatch", async () => {
    const xhr = fakeXhrFactory(() => ({ etag: null }));
    await expect(putMultipart(new Blob(["aaaabbbbcc"]), ticket, { createXhr: xhr.create })).rejects.toThrow(/ETag/);
    await expect(putMultipart(new Blob(["aaaabbbbcc"]), { ...ticket, part_size: 100 }, { createXhr: xhr.create })).rejects.toThrow(
      /expected 1/,
    );
  });

  it("stops other parts when one fails permanently", async () => {
    const xhr = fakeXhrFactory((r) => (r.url.endsWith("p1") ? { status: 403 } : new Promise(() => undefined)));
    await expect(putMultipart(new Blob(["aaaabbbbcc"]), ticket, { createXhr: xhr.create })).rejects.toMatchObject({
      status: 403,
    });
  });
});

describe("helpers", () => {
  it("fitWithin scales the longest side only when needed", () => {
    expect(fitWithin(4000, 2000, 1000)).toEqual({ width: 1000, height: 500 });
    expect(fitWithin(800, 600, 1000)).toBeNull();
  });

  it("backoff grows and is capped", () => {
    expect(backoffDelay(0, 500, 8000, () => 1)).toBe(500);
    expect(backoffDelay(3, 500, 8000, () => 1)).toBe(4000);
    expect(backoffDelay(10, 500, 8000, () => 0)).toBe(4000);
  });

  it("preResize falls back to the original when unsupported", async () => {
    expect(resizeSupported()).toBe(false);
    const f = new File(["x"], "a.png", { type: "image/png" });
    const r = await preResizeImage(f, "a.png", { maxSide: 100 });
    expect(r).toMatchObject({ blob: f, resized: false, filename: "a.png" });
  });

  it("uploadFile with preResize still uploads the original in jsdom", async () => {
    const xhr = fakeXhrFactory(() => ({ status: 200 }));
    const createTicket = vi.fn(() => Promise.resolve(putTicket));
    await uploadFile(new File(["abc"], "p.png", { type: "image/png" }), {
      createTicket,
      complete: () => Promise.resolve(1),
      createXhr: xhr.create,
      preResize: { maxSide: 10 },
    });
    expect(createTicket).toHaveBeenCalledWith({ filename: "p.png", content_type: "image/png", size: 3 });
  });
});
