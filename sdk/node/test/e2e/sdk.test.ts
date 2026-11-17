/**
 * Contract test of @kadr/node against a real Kadr server (fake image engine,
 * in-process dev storage). Run via scripts/e2e-sdk.sh, which starts the
 * server and sets the KADR_E2E_* variables.
 */
import { createHash, randomBytes } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  Kadr,
  KadrError,
  verifySignedUrl,
  type Asset,
  type UploadTicket,
  type WebhookEvent,
} from "../../src/index.js";
import { AdminClient, WebhookReceiver, loadConfig, type ReceivedWebhook } from "./harness.js";

const NAMESPACE = "org_e2e";
const ALL_SCOPES = [
  "uploads:write",
  "assets:read",
  "assets:write",
  "assets:delete",
  "urls:sign",
  "namespaces:admin",
  "imports:write",
];

/**
 * Source bytes: a PNG signature plus random data. The fake engine does not
 * decode images (it reports 1600×1200 and writes placeholder renditions), so
 * the content only has to be unique per run.
 */
function fixture(size = 8192): Buffer {
  return Buffer.concat([Buffer.from("\x89PNG\r\n\x1a\n", "latin1"), randomBytes(size - 8)]);
}

const sha256 = (b: Buffer) => createHash("sha256").update(b).digest("hex");

/** PUTs the file to a ticket exactly as a browser would. */
async function putToTicket(ticket: UploadTicket, body: Buffer): Promise<void> {
  // fetch derives Content-Length from the body; every other header is sent verbatim.
  const headers = Object.fromEntries(
    Object.entries(ticket.headers ?? {}).filter(([k]) => k.toLowerCase() !== "content-length"),
  );
  const res = await fetch(ticket.url!, { method: ticket.method, headers, body });
  if (!res.ok) throw new Error(`PUT to ticket: HTTP ${res.status} ${await res.text()}`);
}

const cfg = loadConfig();
const receiver = new WebhookReceiver();
let kadr: Kadr;
let webhookSecret = "";

function verified(w: ReceivedWebhook): WebhookEvent {
  return kadr.webhooks.verify(w.body, w.headers, webhookSecret);
}

beforeAll(async () => {
  const hookUrl = await receiver.start();
  const admin = new AdminClient(cfg.adminUrl);
  await admin.login(cfg.adminEmail, cfg.adminPassword);
  const project = await admin.post<{ id: string; slug: string }>("/projects", {
    slug: "acme-e2e",
    name: "Acme E2E",
    environment: "test",
  });
  const key = await admin.post<{ secret: string }>(`/projects/${project.id}/api-keys`, {
    name: "e2e",
    scopes: ALL_SCOPES,
  });
  const hook = await admin.post<{ secret: string }>(`/projects/${project.id}/webhooks`, {
    url: hookUrl,
    events: ["*"],
  });
  webhookSecret = hook.secret;
  kadr = new Kadr({
    baseUrl: cfg.publicUrl,
    apiKey: key.secret,
    signing: { kid: "k1", secret: "e2e-signing-secret", baseUrl: "https://media.example.com" },
    maxRetries: 0,
  });
});

afterAll(async () => {
  await receiver.stop();
});

describe("@kadr/node against a live server", () => {
  let ticketAsset: Asset;
  let sourceSha = "";

  it("pings with the project key", async () => {
    const ping = await kadr.ping();
    expect(ping).toMatchObject({ ok: true, project: "acme-e2e", environment: "test" });
    expect(ping.projectId).toMatch(/^prj_/);
    expect(ping.scopes).toEqual(expect.arrayContaining(["uploads:write", "assets:read"]));
  });

  it("uploads via ticket → PUT → complete with wait", async () => {
    const file = fixture();
    sourceSha = sha256(file);
    const created = await kadr.uploads.create(
      {
        preset: "photo-lite",
        namespace: NAMESPACE,
        visibility: "public",
        filename: "listing.png",
        contentType: "image/png",
        size: file.byteLength,
        sha256: sourceSha,
        metadata: { entity: "listing", entity_id: "lst_77" },
      },
      { idempotencyKey: `e2e-${sourceSha}` },
    );
    if (created.kind !== "upload") throw new Error("expected an upload ticket");
    expect(created.uploadId).toMatch(/^upl_/);
    expect(created.method).toBe("PUT");
    expect(created.headers?.["Content-Type"]).toBe("image/png");
    expect(created.headers?.["Content-Length"]).toBe(String(file.byteLength));
    expect(Date.parse(created.expiresAt)).toBeGreaterThan(Date.now());

    // complete before the PUT: upload_incomplete
    const early = await kadr.uploads.complete(created.uploadId).catch((e: unknown) => e);
    expect(early).toBeInstanceOf(KadrError);
    expect(early).toMatchObject({ status: 409, code: "upload_incomplete" });

    await putToTicket(created, file);
    const asset = await kadr.uploads.complete(created.uploadId, { wait: 20 });
    ticketAsset = asset;

    expect(asset).toMatchObject({
      status: "ready",
      kind: "image",
      namespace: NAMESPACE,
      visibility: "public",
      preset: "photo-lite",
      presetVersion: 1,
      original: { filename: "listing.png", contentType: "image/png", bytes: file.byteLength, width: 1600, height: 1200 },
      durationSec: null,
      poster: null,
      metadata: { entity: "listing", entity_id: "lst_77" },
    });
    expect(asset.id).toMatch(/^ast_/);
    expect(asset.error).toBeUndefined();
    expect(asset.readyAt).toEqual(expect.any(String));
    expect(asset.savingsRatio).toEqual(expect.any(Number));
    expect(asset.placeholder?.dominantColor).toMatch(/^#[0-9a-f]{6}$/);
    expect(asset.placeholder?.thumbhash).toEqual(expect.any(String));
    expect(asset.renditions.map((r) => r.format).sort()).toEqual(["avif", "webp"]);
    for (const r of asset.renditions) {
      expect(r).toMatchObject({ name: "main", width: 800, height: 600 });
      expect(r.key).toBeUndefined();
      expect(r.url).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/kadr-public\/acme-e2e\/blb_[0-9a-z]+\/main\.(avif|webp)$/);
      const res = await fetch(r.url!);
      expect(res.status).toBe(200);
      expect((await res.arrayBuffer()).byteLength).toBe(r.bytes);
    }

    // complete is idempotent
    expect((await kadr.uploads.complete(created.uploadId)).id).toBe(asset.id);
    expect((await kadr.assets.get(asset.id)).status).toBe("ready");
  });

  it("deduplicates by sha256 (200 {asset})", async () => {
    const dup = await kadr.uploads.create({
      preset: "photo-lite",
      namespace: NAMESPACE,
      visibility: "public",
      contentType: "image/png",
      size: 8192,
      sha256: sourceSha,
    });
    if (dup.kind !== "asset") throw new Error("expected a deduplicated asset");
    expect(dup.asset.id).not.toBe(ticketAsset.id);
    expect(dup.asset.status).toBe("ready");
    expect(dup.asset.renditions.map((r) => r.url)).toEqual(ticketAsset.renditions.map((r) => r.url));
  });

  it("uploads server-side (private) and signs a rendition key locally", async () => {
    const asset = await kadr.assets.upload(fixture(4096), {
      preset: "doc-lite",
      namespace: NAMESPACE,
      visibility: "private",
      filename: "receipt.png",
      contentType: "image/png",
      metadata: { entity: "receipt" },
      wait: 20,
    });
    expect(asset).toMatchObject({ status: "ready", visibility: "private", preset: "doc-lite" });
    const r = asset.renditions[0]!;
    expect(r.url).toBeUndefined();
    expect(r.key).toMatch(/^acme-e2e\/blb_[0-9a-z]+\/main\.(avif|webp)$/);

    const now = Date.UTC(2026, 10, 16, 21, 0, 0);
    const url = kadr.signUrl(r.key!, { ttl: 3600, download: "receipt copy.avif", now });
    const exp = now / 1000 + 3600;
    expect(url).toMatch(
      new RegExp(
        `^https://media\\.example\\.com/acme-e2e/blb_[0-9a-z]+/main\\.(avif|webp)\\?exp=${exp}&kid=k1&sig=[A-Za-z0-9_-]{43}&download=receipt%20copy\\.avif$`,
      ),
    );
    expect(verifySignedUrl(url, { secrets: { k1: "e2e-signing-secret" }, now })).toEqual({
      ok: true,
      key: r.key,
      exp,
      kid: "k1",
      download: "receipt copy.avif",
    });
  });

  it("lists assets with cursor pagination", async () => {
    const first = await kadr.assets.listPage({ namespace: NAMESPACE, limit: 1 });
    expect(first.items).toHaveLength(1);
    expect(first.nextCursor).toEqual(expect.any(String));

    const ids: string[] = [];
    for await (const a of kadr.assets.list({ namespace: NAMESPACE, limit: 1 })) ids.push(a.id);
    expect(ids).toHaveLength(3);
    expect(new Set(ids).size).toBe(3);
    expect(ids).toContain(ticketAsset.id);
    expect(ids[0]).toBe(first.items[0]!.id);

    const failed = await kadr.assets.listPage({ namespace: NAMESPACE, status: "failed" });
    expect(failed).toEqual({ items: [], nextCursor: null });
  });

  it("lists and gets presets", async () => {
    const presets = await kadr.presets.list();
    const slugs = presets.map((p) => p.slug);
    expect(slugs).toEqual(expect.arrayContaining(["photo-lite", "photo-balanced", "doc-lite", "avatar"]));
    const photo = await kadr.presets.get("photo-lite");
    expect(photo).toMatchObject({ slug: "photo-lite", kind: "image", version: 1 });
    expect(photo.createdAt).toEqual(expect.any(String));
    // params are passed through verbatim (camelCase keys on the wire already)
    expect(photo.params).toHaveProperty("limits.maxInputBytes");

    const missing = await kadr.presets.get("nope").catch((e: unknown) => e);
    expect(missing).toMatchObject({ status: 404, code: "preset_not_found" });
  });

  it("reports namespace usage and sets quotas (quota.warning)", async () => {
    const usage = await kadr.namespaces.usage(NAMESPACE);
    expect(usage).toMatchObject({
      namespace: NAMESPACE,
      assetCount: 3,
      reservedBytes: 0,
      videoSecMonth: 0,
      quotaBytes: null,
      quotaAssets: null,
      quotaVideoSecMonth: null,
    });
    expect(usage.usedBytes).toBeGreaterThan(0);

    // 85 % of the quota → one quota.warning
    const quota = Math.ceil(usage.usedBytes / 0.85);
    const after = await kadr.namespaces.setQuota(NAMESPACE, { quotaBytes: quota });
    expect(after).toMatchObject({ quotaBytes: quota, quotaAssets: null, usedBytes: usage.usedBytes });
    const w = await receiver.waitFor((x) => x.type === "quota.warning");
    const event = verified(w);
    if (event.type !== "quota.warning") throw new Error("wrong type");
    expect(event.data).toMatchObject({ namespace: NAMESPACE, usedBytes: usage.usedBytes, quotaBytes: quota });
    expect(event.data.ratio).toBeGreaterThanOrEqual(0.8);

    expect((await kadr.namespaces.setQuota(NAMESPACE, {})).quotaBytes).toBeNull();
  });

  it("updates metadata (asset.updated)", async () => {
    const updated = await kadr.assets.update(ticketAsset.id, { metadata: { entity: "listing", stage: "sold" } });
    expect(updated.metadata).toEqual({ entity: "listing", stage: "sold" });
    const visibility = await kadr.assets.update(ticketAsset.id, { visibility: "private" }).catch((e: unknown) => e);
    expect(visibility).toMatchObject({ status: 501, code: "not_implemented" });

    const event = verified(await receiver.waitFor((x) => x.type === "asset.updated"));
    if (event.type !== "asset.updated") throw new Error("wrong type");
    expect(event.data.changes).toEqual(["metadata"]);
    expect(event.data.metadata).toEqual({ entity: "listing", stage: "sold" });
  });

  it("deletes an asset", async () => {
    await kadr.assets.delete(ticketAsset.id);
    const gone = await kadr.assets.get(ticketAsset.id).catch((e: unknown) => e);
    expect(gone).toBeInstanceOf(KadrError);
    expect(gone).toMatchObject({ status: 404, code: "not_found" });
    expect((await kadr.namespaces.usage(NAMESPACE)).assetCount).toBe(2);
  });

  it("receives signed asset.ready and asset.deleted webhooks", async () => {
    const isFor = (type: string) => (w: ReceivedWebhook) =>
      w.type === type && w.body.toString("utf8").includes(ticketAsset.id);

    const readyRaw = await receiver.waitFor(isFor("asset.ready"));
    const ready = verified(readyRaw);
    if (ready.type !== "asset.ready") throw new Error("wrong type");
    expect(ready.id).toMatch(/^evt_/);
    expect(ready.id).toBe(readyRaw.headers["webhook-id"]);
    expect(Date.parse(ready.timestamp)).not.toBeNaN();
    expect(ready.data.asset).toMatchObject({ id: ticketAsset.id, status: "ready", presetVersion: 1 });
    expect(ready.data.metadata).toEqual({ entity: "listing", entity_id: "lst_77" });

    const deleted = verified(await receiver.waitFor(isFor("asset.deleted")));
    if (deleted.type !== "asset.deleted") throw new Error("wrong type");
    expect(deleted.data.asset).toMatchObject({ id: ticketAsset.id, status: "deleted" });

    // A tampered body fails verification.
    const tampered = Buffer.from(readyRaw.body.toString("utf8").replace("ready", "failed"));
    expect(() =>
      kadr.webhooks.verify(tampered, readyRaw.headers as Record<string, string>, webhookSecret),
    ).toThrow(/signature/);
  });

  it("maps API errors to KadrError", async () => {
    const invalid = await kadr.uploads
      .create({ preset: "photo-lite", namespace: "bad namespace!", visibility: "public", contentType: "", size: 0 })
      .catch((e: unknown) => e);
    expect(invalid).toBeInstanceOf(KadrError);
    const e = invalid as KadrError;
    expect(e).toMatchObject({ status: 422, code: "validation_failed" });
    expect(Object.keys(e.errors ?? {}).sort()).toEqual(["content_type", "namespace", "size"]);

    expect(await kadr.uploads.complete("upl_00000000000000000000000000").catch((x: unknown) => x)).toMatchObject({
      status: 404,
      code: "not_found",
    });
  });
});
