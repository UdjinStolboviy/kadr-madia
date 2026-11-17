// In-memory implementation of docs/ADMIN_API.md for `VITE_MOCK=1 pnpm dev`.
// It patches window.fetch for /admin/api/* requests only. This module is loaded
// via a dynamic import guarded by import.meta.env.VITE_MOCK, so it is never part
// of a production build.
import type {
  ApiKey,
  ApiKeyScope,
  CreateApiKeyRequest,
  CloudConnection,
  CreateProjectRequest,
  Delivery,
  Job,
  LoginRequest,
  Project,
  SigningKey,
  StorageProfile,
  StorageProfileInput,
  StorageTestResult,
  SystemInfo,
  UpdateProjectRequest,
  WebhookEndpoint,
} from "@/lib/types";
import { API_KEY_SCOPES, SLUG_RE } from "@/lib/types";

import { defaultPresets, seed, ulid, type MockDb } from "./data";

const DB_KEY = "kadr-mock-db";
const SESSION_KEY = "kadr-mock-session";
const VALID_TOTP = "123456";
const STARTED_AT = new Date(Date.now() - 3 * 86400_000 - 4 * 3600_000).toISOString();

// ---- State ------------------------------------------------------------------

function load(): MockDb {
  const params = new URLSearchParams(window.location.search);
  const mode = params.get("mock");
  if (mode === "empty" || mode === "reset") {
    const db = seed(mode === "empty");
    sessionStorage.setItem(DB_KEY, JSON.stringify(db));
    params.delete("mock");
    const qs = params.toString();
    window.history.replaceState(null, "", window.location.pathname + (qs ? `?${qs}` : "") + window.location.hash);
    return db;
  }
  try {
    const raw = sessionStorage.getItem(DB_KEY);
    if (raw) return JSON.parse(raw) as MockDb;
  } catch {
    /* ignore */
  }
  return seed(false);
}

let db: MockDb;
const persist = () => sessionStorage.setItem(DB_KEY, JSON.stringify(db));

interface Session {
  csrf: string;
}
function getSession(): Session | null {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    return null;
  }
}
function setSession(s: Session | null) {
  if (s) sessionStorage.setItem(SESSION_KEY, JSON.stringify(s));
  else sessionStorage.removeItem(SESSION_KEY);
}

const loginFailures: number[] = [];

// ---- Helpers ----------------------------------------------------------------

class HttpError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly title: string,
    readonly detail?: string,
    readonly errors?: Record<string, string>,
  ) {
    super(title);
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const nowIso = () => new Date().toISOString();

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
function problem(e: HttpError): Response {
  return new Response(
    JSON.stringify({
      type: "about:blank",
      title: e.title,
      status: e.status,
      code: e.code,
      ...(e.detail ? { detail: e.detail } : {}),
      ...(e.errors ? { errors: e.errors } : {}),
    }),
    { status: e.status, headers: { "Content-Type": "application/problem+json" } },
  );
}
const noContent = () => new Response(null, { status: 204 });

function audit(action: string, target: string | null, project_id: string | null = null, details: unknown = null) {
  db.audit.unshift({
    id: `aud_${Date.now().toString(36)}${(db.seq++).toString(36)}`,
    actor_type: "admin",
    actor_id: db.admin.id,
    project_id,
    action,
    target,
    details,
    ip: "127.0.0.1",
    created_at: nowIso(),
  });
}

function publicAdmin() {
  const { password: _p, totp_secret: _t, pending_totp_secret: _pt, ...admin } = db.admin;
  return admin;
}

function publicProfile(p: MockDb["storage"][number]): StorageProfile {
  const { secret: _s, access_key_full: _a, ...rest } = p;
  return rest;
}

function mask(key: string) {
  return key.length > 8 ? `${key.slice(0, 4)}…${key.slice(-4)}` : "••••";
}

function randomBase62(n: number) {
  const a = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let s = "";
  for (let i = 0; i < n; i++) s += a[Math.floor(Math.random() * a.length)];
  return s;
}

function findProject(id: string): Project {
  const p = db.projects.find((x) => x.id === id);
  if (!p) throw new HttpError(404, "not_found", "Not found", "Project not found");
  return p;
}

function findProfile(id: string) {
  const p = db.storage.find((x) => x.id === id);
  if (!p) throw new HttpError(404, "not_found", "Not found", "Storage profile not found");
  return p;
}

function validationError(errors: Record<string, string>) {
  return new HttpError(422, "validation_failed", "Validation failed", "Some fields are invalid", errors);
}

// ---- Routes -----------------------------------------------------------------

type Handler = (ctx: { params: string[]; body: unknown; query: URLSearchParams }) => Promise<Response> | Response;
interface RouteDef {
  method: string;
  re: RegExp;
  handler: Handler;
  public?: boolean;
}

const routes: RouteDef[] = [];
const on = (method: string, path: string, handler: Handler, isPublic = false) => {
  const re = new RegExp(`^${path.replace(/:([a-z_]+)/g, "([^/]+)")}$`);
  routes.push({ method, re, handler, public: isPublic });
};

on(
  "POST",
  "/auth/login",
  async ({ body }) => {
    await sleep(350);
    const b = body as LoginRequest;
    const cutoff = Date.now() - 60_000;
    while (loginFailures.length && loginFailures[0]! < cutoff) loginFailures.shift();
    if (loginFailures.length >= 5) throw new HttpError(429, "rate_limited", "Too many requests");
    if (b.email?.toLowerCase() !== db.admin.email || b.password !== db.admin.password) {
      loginFailures.push(Date.now());
      throw new HttpError(401, "invalid_credentials", "Invalid credentials");
    }
    if (db.admin.totp_enabled) {
      if (!b.totp_code) throw new HttpError(401, "totp_required", "TOTP code required");
      if (b.totp_code !== VALID_TOTP) {
        loginFailures.push(Date.now());
        throw new HttpError(401, "invalid_credentials", "Invalid credentials");
      }
    }
    const session = { csrf: randomBase62(32) };
    setSession(session);
    db.admin.last_login_at = nowIso();
    audit("auth.login", db.admin.email, null, { method: db.admin.totp_enabled ? "password+totp" : "password" });
    persist();
    return json(200, { admin: publicAdmin(), csrf_token: session.csrf });
  },
  true,
);

on("POST", "/auth/logout", () => {
  setSession(null);
  return noContent();
});

on("GET", "/auth/me", () => json(200, { admin: publicAdmin(), csrf_token: getSession()!.csrf }));

on("POST", "/auth/totp/setup", () => {
  const secret = Array.from({ length: 32 }, () => "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"[Math.floor(Math.random() * 32)]).join("");
  db.admin.pending_totp_secret = secret;
  persist();
  const label = encodeURIComponent(`Kadr:${db.admin.email}`);
  return json(200, { secret, otpauth_url: `otpauth://totp/${label}?secret=${secret}&issuer=Kadr&digits=6&period=30` });
});

on("POST", "/auth/totp/enable", ({ body }) => {
  const { code } = body as { code: string };
  if (!db.admin.pending_totp_secret || code !== VALID_TOTP) throw new HttpError(400, "invalid_code", "Invalid code");
  db.admin.totp_secret = db.admin.pending_totp_secret;
  db.admin.pending_totp_secret = null;
  db.admin.totp_enabled = true;
  audit("auth.totp_enable", db.admin.email);
  persist();
  return noContent();
});

on("POST", "/auth/totp/disable", ({ body }) => {
  const { password } = body as { password: string };
  if (password !== db.admin.password) throw new HttpError(401, "invalid_credentials", "Invalid credentials");
  db.admin.totp_enabled = false;
  db.admin.totp_secret = null;
  audit("auth.totp_disable", db.admin.email);
  persist();
  return noContent();
});

// Projects
on("GET", "/projects", () => json(200, { items: db.projects }));

on("POST", "/projects", ({ body }) => {
  const b = body as CreateProjectRequest;
  const errors: Record<string, string> = {};
  if (!SLUG_RE.test(b.slug ?? "")) errors.slug = "Invalid slug";
  if (!b.name?.trim()) errors.name = "Required";
  if (b.environment !== "live" && b.environment !== "test") errors.environment = "Must be live or test";
  if (b.storage_profile_id && !db.storage.some((s) => s.id === b.storage_profile_id))
    errors.storage_profile_id = "Unknown storage profile";
  if (Object.keys(errors).length) throw validationError(errors);
  if (db.projects.some((p) => p.slug === b.slug)) throw new HttpError(409, "slug_taken", "Slug already taken");
  const ts = nowIso();
  const project: Project = {
    id: ulid("prj_"),
    slug: b.slug,
    name: b.name.trim(),
    environment: b.environment,
    storage_profile_id: b.storage_profile_id ?? db.storage.find((s) => s.is_platform)?.id ?? null,
    allowed_origins: b.allowed_origins ?? [],
    quota_bytes: b.quota_bytes ?? null,
    used_bytes: 0,
    disabled: false,
    created_at: ts,
    updated_at: ts,
  };
  db.projects.push(project);
  db.presets[project.id] = defaultPresets(project.id, ts);
  audit("project.create", project.id, project.id, { slug: project.slug });
  persist();
  return json(201, project);
});

on("GET", "/projects/:id", ({ params }) => json(200, findProject(params[0]!)));

on("PATCH", "/projects/:id", ({ params, body }) => {
  const p = findProject(params[0]!);
  const b = body as UpdateProjectRequest;
  if (b.name !== undefined && !b.name.trim()) throw validationError({ name: "Required" });
  if (b.storage_profile_id && !db.storage.some((s) => s.id === b.storage_profile_id))
    throw validationError({ storage_profile_id: "Unknown storage profile" });
  Object.assign(p, b, { updated_at: nowIso() });
  const action = b.disabled === true ? "project.disable" : b.disabled === false ? "project.enable" : "project.update";
  audit(action, p.id, p.id, { changed: Object.keys(b) });
  persist();
  return json(200, p);
});

on("GET", "/projects/:id/presets", ({ params }) => {
  findProject(params[0]!);
  return json(200, { items: db.presets[params[0]!] ?? [] });
});

// API keys
on("GET", "/projects/:id/api-keys", ({ params }) => {
  findProject(params[0]!);
  return json(200, { items: db.apiKeys.filter((k) => k.project_id === params[0]) });
});

on("POST", "/projects/:id/api-keys", ({ params, body }) => {
  const p = findProject(params[0]!);
  const b = body as CreateApiKeyRequest;
  const errors: Record<string, string> = {};
  if (!b.name?.trim()) errors.name = "Required";
  if (!Array.isArray(b.scopes) || !b.scopes.length) errors.scopes = "At least one scope is required";
  else if (b.scopes.some((s) => !API_KEY_SCOPES.includes(s as ApiKeyScope))) errors.scopes = "Unknown scope";
  if (b.expires_at && new Date(b.expires_at).getTime() <= Date.now()) errors.expires_at = "Must be in the future";
  if (Object.keys(errors).length) throw validationError(errors);
  const short = randomBase62(8);
  const prefix = `kd_${p.environment}_${short}`;
  const key: ApiKey = {
    id: ulid("key_"),
    project_id: p.id,
    name: b.name.trim(),
    prefix,
    scopes: b.scopes,
    last_used_at: null,
    expires_at: b.expires_at ?? null,
    revoked_at: null,
    created_at: nowIso(),
  };
  db.apiKeys.push(key);
  audit("api_key.create", key.id, p.id, { name: key.name, scopes: key.scopes });
  persist();
  return json(201, { api_key: key, secret: `${prefix}_${randomBase62(32)}` });
});

on("DELETE", "/api-keys/:id", ({ params }) => {
  const k = db.apiKeys.find((x) => x.id === params[0]);
  if (!k) throw new HttpError(404, "not_found", "Not found");
  k.revoked_at ??= nowIso();
  audit("api_key.revoke", k.id, k.project_id, { name: k.name });
  persist();
  return noContent();
});

// Storage profiles
on("GET", "/storage-profiles", () => json(200, { items: db.storage.map(publicProfile) }));

on("POST", "/storage-profiles", async ({ body }) => {
  await sleep(250);
  const b = body as StorageProfileInput;
  const errors: Record<string, string> = {};
  for (const k of ["name", "endpoint", "region", "public_bucket", "private_bucket", "public_base_url", "access_key_id", "secret_access_key"] as const) {
    if (!b[k]) errors[k] = "Required";
  }
  if (Object.keys(errors).length) throw validationError(errors);
  const ts = nowIso();
  if (b.is_platform) db.storage.forEach((s) => (s.is_platform = false));
  const { secret_access_key, access_key_id, ...rest } = b;
  const profile = {
    ...rest,
    id: ulid("stp_"),
    access_key_id: mask(access_key_id),
    access_key_full: access_key_id,
    secret: secret_access_key,
    last_test_at: null,
    last_test_ok: null,
    created_at: ts,
    updated_at: ts,
  };
  db.storage.push(profile);
  audit("storage_profile.create", profile.id, null, { provider: profile.provider });
  persist();
  return json(201, publicProfile(profile));
});

on("GET", "/storage-profiles/:id", ({ params }) => json(200, publicProfile(findProfile(params[0]!))));

on("PATCH", "/storage-profiles/:id", async ({ params, body }) => {
  await sleep(250);
  const p = findProfile(params[0]!);
  const { secret_access_key, access_key_id, ...rest } = body as Partial<StorageProfileInput>;
  if (rest.is_platform) db.storage.forEach((s) => (s.is_platform = false));
  Object.assign(p, rest, { updated_at: nowIso() });
  if (access_key_id) {
    p.access_key_full = access_key_id;
    p.access_key_id = mask(access_key_id);
  }
  if (secret_access_key) p.secret = secret_access_key;
  audit("storage_profile.update", p.id, null, { changed: Object.keys(body as object) });
  persist();
  return json(200, publicProfile(p));
});

on("DELETE", "/storage-profiles/:id", ({ params }) => {
  const p = findProfile(params[0]!);
  if (db.projects.some((x) => x.storage_profile_id === p.id))
    throw new HttpError(409, "in_use", "Storage profile in use", "One or more projects use this storage profile");
  db.storage = db.storage.filter((x) => x.id !== p.id);
  audit("storage_profile.delete", p.id);
  persist();
  return noContent();
});

on("POST", "/storage-profiles/:id/test", async ({ params }) => {
  const p = findProfile(params[0]!);
  await sleep(1400);
  // Profiles whose public URL mentions "archive" or "fail" simulate a broken custom domain.
  const brokenPublic = /archive|fail/i.test(p.public_base_url);
  const names = [
    "private_put",
    "private_head",
    "private_delete",
    "private_presign_put",
    "public_put",
    "public_get_via_base_url",
    "public_delete",
  ];
  const checks = names.map((name) => {
    const fail = brokenPublic && name === "public_get_via_base_url";
    return {
      name,
      ok: !fail,
      detail: fail
        ? `GET ${p.public_base_url}/.kadr-test/… returned 404 — is the custom domain attached to bucket "${p.public_bucket}"?`
        : name.startsWith("private")
          ? `bucket ${p.private_bucket}`
          : `bucket ${p.public_bucket}`,
      duration_ms: Math.round(40 + Math.random() * (name.includes("get") ? 300 : 140)),
    };
  });
  const result: StorageTestResult = { ok: checks.every((c) => c.ok), checks };
  p.last_test_at = nowIso();
  p.last_test_ok = result.ok;
  audit("storage_profile.test", p.id, null, { ok: result.ok });
  persist();
  return json(200, result);
});

// System
on("GET", "/system", () => {
  const sys: SystemInfo = {
    version: "v0.1.0-dev",
    commit: "3f9a1c27e8b4d60f5a2e91c7b38d4f0e6a1b2c3d",
    started_at: STARTED_AT,
    uptime_sec: Math.floor((Date.now() - new Date(STARTED_AT).getTime()) / 1000),
    go_version: "go1.25.3",
    dev: true,
    db: { path: "/var/lib/kadr/kadr.db", size_bytes: 14_862_336, wal_bytes: 1_204_224 },
    tmp: { path: "/var/lib/kadr/tmp", free_bytes: 41_231_686_042 },
    capabilities: {
      vips: { ok: true, version: "8.16.0" },
      vips_avif: { ok: true, version: "libheif 1.19.5 (aom)" },
      ffmpeg: { ok: true, version: "7.1" },
      ffprobe: { ok: true, version: "7.1" },
      rclone: { ok: false, detail: "rclone not found in PATH" },
      vaapi: { ok: false, detail: "/dev/dri/renderD128 not present" },
    },
  };
  return json(200, sys);
});

// Jobs, assets, analytics, webhooks, signing keys and cloud connections: in-memory, not persisted.
// The jobs SSE stream is not mocked; the Jobs page falls back to polling.
const mockJobs: Job[] = [
  { id: "job_01", kind: "process_image", pool: "image", status: "succeeded", attempts: 1, max_attempts: 5, progress: null, last_error: null, run_at: nowIso(), created_at: nowIso(), finished_at: nowIso() },
  { id: "job_02", kind: "process_video", pool: "video", status: "running", attempts: 1, max_attempts: 3, progress: 42, last_error: null, run_at: nowIso(), created_at: nowIso(), finished_at: null },
  { id: "job_03", kind: "process_image", pool: "image", status: "dead", attempts: 5, max_attempts: 5, progress: null, last_error: "decode failed: unsupported image format", run_at: nowIso(), created_at: nowIso(), finished_at: nowIso() },
  { id: "job_04", kind: "gc_blob", pool: "maintenance", status: "queued", attempts: 0, max_attempts: 5, progress: null, last_error: null, run_at: nowIso(), created_at: nowIso(), finished_at: null },
];
on("GET", "/jobs", ({ query }) => {
  const st = query.get("status");
  const pool = query.get("pool");
  return json(200, { items: mockJobs.filter((j) => (!st || j.status === st) && (!pool || j.pool === pool)) });
});
on("POST", "/jobs/:id/retry", ({ params }) => {
  const j = mockJobs.find((x) => x.id === params[0]);
  if (!j) throw new HttpError(404, "not_found", "Not found", "job not found");
  if (j.status !== "dead" && j.status !== "cancelled")
    throw new HttpError(409, "invalid_state", "Conflict", "only dead or cancelled jobs can be retried");
  j.status = "queued";
  j.finished_at = null;
  return json(200, j);
});
on("POST", "/jobs/:id/cancel", ({ params }) => {
  const j = mockJobs.find((x) => x.id === params[0]);
  if (!j) throw new HttpError(404, "not_found", "Not found", "job not found");
  if (j.status !== "queued" && j.status !== "running") throw new HttpError(409, "invalid_state", "Conflict", "the job already finished");
  j.status = "cancelled";
  j.finished_at = nowIso();
  return json(200, j);
});

on("GET", "/projects/:id/assets", ({ params }) => {
  findProject(params[0]!);
  return json(200, { items: [], next_cursor: null });
});
on("GET", "/projects/:id/analytics", ({ params, query }) => {
  findProject(params[0]!);
  const n = Number(query.get("days") ?? 30);
  const days = Array.from({ length: n }, (_, i) => {
    const day = new Date(Date.now() - (n - 1 - i) * 86400_000).toISOString().slice(0, 10);
    const uploads = 20 + ((i * 37) % 60);
    const bytes_in = uploads * 2_400_000;
    return { day, uploads, assets_ready: uploads - (i % 7 === 0 ? 2 : 0), assets_failed: i % 7 === 0 ? 2 : 0, bytes_in, bytes_stored: Math.round(bytes_in * 0.12), video_sec: i % 3 === 0 ? 90 : 0, cpu_ms: uploads * 400, dedup_hits: i % 5 };
  });
  const sum = (k: keyof (typeof days)[number]) => days.reduce((a, d) => a + (d[k] as number), 0);
  const totals = { day: "", uploads: sum("uploads"), assets_ready: sum("assets_ready"), assets_failed: sum("assets_failed"), bytes_in: sum("bytes_in"), bytes_stored: sum("bytes_stored"), video_sec: sum("video_sec"), cpu_ms: sum("cpu_ms"), dedup_hits: sum("dedup_hits") };
  return json(200, {
    from: days[0]!.day,
    to: days[days.length - 1]!.day,
    days,
    totals,
    savings_ratio: 1 - totals.bytes_stored / totals.bytes_in,
    failed_rate: totals.assets_failed / (totals.assets_ready + totals.assets_failed),
  });
});
on("POST", "/playground", () => {
  throw new HttpError(503, "unavailable", "Service Unavailable", "the image engine is not available in mock mode");
});
on("POST", "/projects/:id/presets/:slug/versions", ({ params, body }) => {
  const list = (db.presets[findProject(params[0]!).id] ??= []);
  const b = body as { params: Record<string, unknown>; description?: string };
  let p = list.find((x) => x.slug === params[1]);
  if (p) {
    p.current_version += 1;
    p.params = b.params;
  } else {
    p = { id: ulid("pst_"), slug: params[1]!, kind: "image", description: b.description ?? "", current_version: 1, params: b.params, created_at: nowIso() };
    list.push(p);
  }
  audit("preset.version", p.id, params[0]!, { slug: p.slug, version: p.current_version });
  persist();
  return json(200, p);
});

const mockWebhooks: WebhookEndpoint[] = [];
on("GET", "/projects/:id/webhooks", ({ params }) => json(200, { items: mockWebhooks.filter((w) => w.project_id === params[0]) }));
on("POST", "/projects/:id/webhooks", ({ params, body }) => {
  const b = body as { url?: string; events?: string[] };
  if (!/^https?:\/\//.test(b.url ?? "")) throw validationError({ url: "must be an absolute http(s) URL without credentials" });
  const w: WebhookEndpoint = { id: ulid("whe_"), project_id: findProject(params[0]!).id, url: b.url!, events: b.events?.length ? b.events : ["*"], disabled: false, created_at: nowIso() };
  mockWebhooks.push(w);
  return json(201, { webhook: w, secret: `whsec_${randomBase62(32)}` });
});
on("DELETE", "/webhooks/:id", ({ params }) => {
  const i = mockWebhooks.findIndex((w) => w.id === params[0]);
  if (i >= 0) mockWebhooks.splice(i, 1);
  return noContent();
});
on("POST", "/webhooks/:id/test", ({ params }) => {
  const w = mockWebhooks.find((x) => x.id === params[0]);
  if (!w) throw new HttpError(404, "not_found", "Not found", "webhook endpoint not found");
  const d: Delivery = { id: ulid("dlv_"), event_id: ulid("evt_"), event_type: "webhook.test", endpoint_id: w.id, endpoint_url: w.url, status: "delivered", attempts: 1, next_attempt_at: null, last_status_code: 200, last_error: null, last_duration_ms: 84, delivered_at: nowIso(), created_at: nowIso() };
  mockDeliveries.unshift(d);
  return json(200, d);
});
const mockDeliveries: Delivery[] = [];
on("GET", "/projects/:id/deliveries", ({ params, query }) => {
  const ids = new Set(mockWebhooks.filter((w) => w.project_id === params[0]).map((w) => w.id));
  const st = query.get("status");
  return json(200, { items: mockDeliveries.filter((d) => ids.has(d.endpoint_id) && (!st || d.status === st)), next_cursor: null });
});
on("POST", "/deliveries/:id/retry", ({ params }) => {
  const d = mockDeliveries.find((x) => x.id === params[0]);
  if (!d) throw new HttpError(404, "not_found", "Not found", "delivery not found");
  d.status = "pending";
  return json(200, d);
});

const mockSigning: SigningKey[] = [];
on("GET", "/projects/:id/signing-keys", ({ params }) => json(200, { items: mockSigning.filter((k) => k.project_id === params[0]) }));
on("POST", "/projects/:id/signing-keys", ({ params }) => {
  const p = findProject(params[0]!);
  const mine = mockSigning.filter((k) => k.project_id === p.id);
  for (const k of mine) if (k.status === "active") k.status = "verify_only";
  const key: SigningKey = { id: `${p.slug}-k${mine.length + 1}`, project_id: p.id, status: "active", published_at: null, created_at: nowIso() };
  mockSigning.push(key);
  return json(201, { key, secret: randomBase62(43) });
});
on("DELETE", "/signing-keys/:kid", ({ params }) => {
  const k = mockSigning.find((x) => x.id === params[0]);
  if (k) k.status = "retired";
  return noContent();
});

const mockCloud: CloudConnection[] = [];
on("GET", "/projects/:id/cloud-connections", ({ params }) => json(200, { items: mockCloud.filter((c) => c.project_id === params[0]) }));
on("POST", "/projects/:id/cloud-connections", ({ params, body }) => {
  const b = body as { provider: string; token: string; account_label?: string; namespace?: string };
  try {
    if (!(JSON.parse(b.token) as Record<string, unknown>).access_token) throw new Error();
  } catch {
    throw validationError({ token: "must be the rclone token JSON containing access_token" });
  }
  const c: CloudConnection = { id: ulid("cc_"), project_id: findProject(params[0]!).id, namespace: b.namespace ?? null, provider: b.provider, account_label: b.account_label ?? null, created_at: nowIso() };
  mockCloud.push(c);
  return json(201, c);
});
on("DELETE", "/cloud-connections/:id", ({ params }) => {
  const i = mockCloud.findIndex((c) => c.id === params[0]);
  if (i >= 0) mockCloud.splice(i, 1);
  return noContent();
});

// Audit
on("GET", "/audit", ({ query }) => {
  const limit = Math.min(200, Math.max(1, Number(query.get("limit") ?? 50)));
  const cursor = query.get("cursor");
  const start = cursor ? Number(cursor) : 0;
  const items = db.audit.slice(start, start + limit);
  const next = start + limit < db.audit.length ? String(start + limit) : null;
  return json(200, { items, next_cursor: next });
});

// ---- Installation -----------------------------------------------------------

const MUTATING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

async function handle(req: Request): Promise<Response> {
  const url = new URL(req.url);
  const path = url.pathname.replace(/^\/admin\/api/, "");
  const method = req.method.toUpperCase();
  await sleep(120 + Math.random() * 280);

  const match = routes
    .filter((r) => r.method === method)
    .map((r) => ({ r, m: path.match(r.re) }))
    .find((x) => x.m);
  try {
    if (!match) throw new HttpError(404, "not_found", "Not found", `${method} ${path}`);
    const session = getSession();
    if (!match.r.public) {
      if (!session) throw new HttpError(401, "unauthenticated", "Unauthenticated");
      if (MUTATING.has(method) && req.headers.get("X-CSRF-Token") !== session.csrf)
        throw new HttpError(403, "csrf_failed", "CSRF token mismatch");
    }
    const text = await req.text();
    // Multipart bodies (playground) are not parsed by the mock.
    const isJson = (req.headers.get("Content-Type") ?? "").includes("json");
    const body = text && isJson ? (JSON.parse(text) as unknown) : undefined;
    return await match.r.handler({ params: match.m!.slice(1), body, query: url.searchParams });
  } catch (e) {
    if (e instanceof HttpError) return problem(e);
    console.error("[mock]", e);
    return problem(new HttpError(500, "internal", "Internal error", String(e)));
  }
}

export function installMockApi() {
  db = load();
  persist();
  const realFetch = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(input, init);
    const url = new URL(req.url, window.location.origin);
    if (url.origin === window.location.origin && url.pathname.startsWith("/admin/api/")) return handle(req);
    return realFetch(input, init);
  };
  console.info(
    `%c[kadr] mock API enabled%c — sign in as ${db.admin.email} / ${db.admin.password}` +
      ` (TOTP code ${VALID_TOTP}). Append ?mock=empty or ?mock=reset to reseed.`,
    "color:#6366f1;font-weight:bold",
    "color:inherit",
  );
}
