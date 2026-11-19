// In-memory implementation of docs/ADMIN_API.md for `VITE_MOCK=1 pnpm dev`.
// It patches window.fetch for /admin/api/* requests only. This module is loaded
// via a dynamic import guarded by import.meta.env.VITE_MOCK, so it is never part
// of a production build.
import type {
  Agent,
  AgentApproval,
  AgentInput,
  AgentLevel,
  AgentRun,
  AgentSchedule,
  AuditEntry,
  ModerationSettings,
  ScheduleInput,
  Moderation,
  ModerationItem,
  SetModerationRequest,
  ApiKey,
  Asset,
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

import { defaultPresets, modePresets, seed, ulid, type MockDb } from "./data";

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

// ---- WebAuthn (mock: no real ceremony; the browser prompt is skipped) -------
const mockPasskeys: { id: string; name: string; transports: string[]; created_at: string; last_used_at: string | null }[] = [];
const ceremonyOptions = () => ({
  ceremony_id: randomBase62(32),
  options: { publicKey: { challenge: "bW9jay1jaGFsbGVuZ2U", rp: { id: "localhost", name: "Kadr" }, user: { id: "bW9jaw", name: "admin", displayName: "admin" }, pubKeyCredParams: [{ type: "public-key", alg: -7 }], allowCredentials: [] } },
});
on("POST", "/auth/webauthn/login/begin", () => json(200, ceremonyOptions()), true);
on("POST", "/auth/webauthn/login/finish", () => {
  if (mockPasskeys.length === 0) throw new HttpError(401, "invalid_credentials", "passkey sign-in failed");
  const session = { csrf: randomBase62(32) };
  setSession(session);
  db.admin.last_login_at = nowIso();
  audit("auth.login", db.admin.email, null, { method: "passkey" });
  persist();
  return json(200, { admin: publicAdmin(), csrf_token: session.csrf });
}, true);
on("POST", "/auth/webauthn/register/begin", () => json(200, ceremonyOptions()));
on("POST", "/auth/webauthn/register/finish", ({ body }) => {
  const b = body as { name?: string };
  const k = { id: `wak_${randomBase62(10).toLowerCase()}`, name: b.name ?? "Passkey", transports: ["internal"], created_at: nowIso(), last_used_at: null };
  mockPasskeys.push(k);
  audit("auth.webauthn_register", k.id);
  return json(201, k);
});
on("GET", "/auth/webauthn/credentials", () => json(200, { data: mockPasskeys }));
on("DELETE", "/auth/webauthn/credentials/:id", ({ params }) => {
  const i = mockPasskeys.findIndex((k) => k.id === params[0]);
  if (i < 0) throw new HttpError(404, "not_found", "passkey not found");
  mockPasskeys.splice(i, 1);
  return noContent();
});

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

const MODE_DESCR: Record<string, string> = {
  compact: "Good quality at a very small size",
  balanced: "The golden middle",
  quality: "Ideal quality at the smallest size that achieves it",
};
on("GET", "/projects/:id/modes", ({ params }) => {
  const p = findProject(params[0]!);
  const list = (db.presets[p.id] ??= []);
  const defaults = modePresets(p.id, nowIso());
  return json(200, {
    default_mode: p.default_mode ?? "balanced",
    modes: ["compact", "balanced", "quality"].map((m) => {
      const cur = (kind: string) => list.find((x) => x.slug === `${kind}-${m}`) ?? defaults.find((x) => x.slug === `${kind}-${m}`)!;
      const edited = (kind: string) => {
        const c = cur(kind);
        const d = defaults.find((x) => x.slug === c.slug)!;
        return c.current_version > 1 && JSON.stringify(c.params) !== JSON.stringify(d.params);
      };
      return { mode: m, description: MODE_DESCR[m], image: cur("image"), video: cur("video"), file: cur("file"), customized: { image: edited("image"), video: edited("video"), file: edited("file") } };
    }),
  });
});
on("POST", "/projects/:id/presets/:slug/reset", ({ params }) => {
  const p = findProject(params[0]!);
  const list = (db.presets[p.id] ??= []);
  const def = [...modePresets(p.id, nowIso()), ...defaultPresets(p.id, nowIso())].find((x) => x.slug === params[1]);
  if (!def) throw validationError({ slug: "only built-in presets can be reset" });
  let cur = list.find((x) => x.slug === def.slug);
  if (cur) {
    cur.current_version += 1;
    cur.params = def.params;
    cur.description = def.description;
  } else {
    cur = def;
    list.push(cur);
  }
  audit("preset.version", cur.id, p.id, { slug: cur.slug, version: cur.current_version, reset: true });
  persist();
  return json(200, cur);
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
  const namespaces: string[] = [];
  if (Array.isArray(b.namespaces) && b.namespaces.length) {
    if (b.namespaces.length > 100) errors.namespaces = "At most 100 namespaces are allowed";
    else {
      const seen = new Set<string>();
      for (const raw of b.namespaces) {
        const ns = typeof raw === "string" ? raw.trim() : "";
        if (!/^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$/.test(ns)) {
          errors.namespaces = `Invalid namespace "${String(raw)}"`;
          break;
        }
        if (!seen.has(ns)) {
          seen.add(ns);
          namespaces.push(ns);
        }
      }
    }
  }
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
    namespaces,
    last_used_at: null,
    expires_at: b.expires_at ?? null,
    revoked_at: null,
    created_at: nowIso(),
  };
  db.apiKeys.push(key);
  audit("api_key.create", key.id, p.id, {
    name: key.name,
    scopes: key.scopes,
    ...(namespaces.length ? { namespaces } : {}),
  });
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
      ghostscript: { ok: true, version: "10.05.1" },
      qpdf: { ok: true, version: "12.2.0" },
      pdftotext: { ok: true, version: "26.11.0" },
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

const mockDoc: Asset = {
  id: "ast_01JDOC000000000000000000AB",
  status: "ready",
  kind: "file",
  namespace: "contracts",
  visibility: "public",
  preset: "file-balanced",
  preset_version: 1,
  original: { filename: "annual-report-2026.pdf", content_type: "application/pdf", bytes: 8_400_000, width: null, height: null },
  renditions: [
    { name: "main", format: "pdf", bytes: 2_350_000, width: null, height: null, url: "https://cdn.example.com/p/contracts/annual-report-2026.pdf" },
    { name: "thumb", format: "avif", bytes: 14_200, width: 452, height: 640, url: "data:image/svg+xml;utf8,%3Csvg xmlns='http://www.w3.org/2000/svg' width='452' height='640'%3E%3Crect width='100%25' height='100%25' fill='%23e8ecf4'/%3E%3Crect x='50' y='60' width='352' height='24' fill='%23a5b0c8'/%3E%3Crect x='50' y='110' width='300' height='12' fill='%23c5cde0'/%3E%3Crect x='50' y='135' width='340' height='12' fill='%23c5cde0'/%3E%3C/svg%3E" },
  ],
  placeholder: null,
  duration_sec: null,
  poster: null,
  metadata: { pages: 24, optimize: { strategy: "downsample", linearized: true } },
  savings_ratio: 0.72,
  created_at: nowIso(),
  ready_at: nowIso(),
};
const mockPrivateDoc: Asset = {
  ...mockDoc,
  id: "ast_01JDOC000000000000000000AC",
  namespace: "hr",
  visibility: "private",
  preset: "file-quality",
  original: { ...mockDoc.original, filename: "payroll-q3.xlsx", content_type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", bytes: 1_200_000 },
  renditions: [{ name: "main", format: "xlsx", bytes: 1_200_000, width: null, height: null, key: "prv/hr/payroll-q3.xlsx" }],
  savings_ratio: 0,
  metadata: {},
};
on("GET", "/projects/:id/assets", ({ params, query }) => {
  findProject(params[0]!);
  const kind = query.get("kind");
  const items = [mockDoc, mockPrivateDoc].filter((a) => !kind || a.kind === kind);
  return json(200, { items, next_cursor: null });
});
on("GET", "/projects/:id/assets/:aid", ({ params }) => {
  findProject(params[0]!);
  const a = [mockDoc, mockPrivateDoc].find((x) => x.id === params[1]);
  if (!a) throw new HttpError(404, "not_found", "Not found", "asset not found");
  return json(200, a);
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
  const kind = p?.kind ?? (b.params.kind === "video" ? "video" : b.params.kind === "file" ? "file" : "image");
  const bad = kind === "video" ? validateVideo(b.params) : kind === "file" ? validateFile(b.params) : validateImage(b.params);
  if (bad) throw validationError({ params: bad });
  if (p) {
    p.current_version += 1;
    p.params = b.params;
  } else {
    p = { id: ulid("pst_"), slug: params[1]!, kind, description: b.description ?? "", current_version: 1, params: b.params, created_at: nowIso() };
    list.push(p);
  }
  audit("preset.version", p.id, params[0]!, { slug: p.slug, version: p.current_version });
  persist();
  return json(200, p);
});

function validateImage(p: Record<string, unknown>): string {
  const r = (p.resize ?? {}) as { maxWidth?: number; maxHeight?: number };
  const q = (p.quality ?? {}) as { mode?: string; maxBytes?: number };
  const outs = Array.isArray(p.outputs) ? p.outputs : [];
  if (outs.length === 0 || outs.length > 4) return "1–4 outputs required";
  if (!r.maxWidth || !r.maxHeight || r.maxWidth < 16 || r.maxWidth > 8192 || r.maxHeight < 16 || r.maxHeight > 8192) return "resize.maxWidth/maxHeight must be 16–8192";
  if (q.mode === "budget" && !(q.maxBytes && q.maxBytes >= 1024 && q.maxBytes <= 10 << 20)) return "quality.maxBytes must be 1 KB–10 MB";
  if (!["budget", "ssim", "fixed"].includes(q.mode ?? "")) return "quality.mode must be budget, ssim or fixed";
  return "";
}
function validateFile(p: Record<string, unknown>): string {
  const lim = (p.limits ?? {}) as { maxInputBytes?: number; accept?: string[] };
  const o = (p.optimize ?? {}) as { strategy?: string; imageDpi?: number; jpegQuality?: number; minSavingsPercent?: number };
  const th = (p.thumbnail ?? {}) as { enabled?: boolean; maxSide?: number; maxBytes?: number };
  const formats = ["pdf", "docx", "xlsx", "pptx", "odt", "ods", "odp", "rtf", "txt", "csv"];
  if ((lim.maxInputBytes ?? 0) > 5 * 2 ** 30) return "limits.maxInputBytes must be at most 5 GB";
  for (const a of lim.accept ?? []) if (!formats.includes(a)) return "limits.accept: unsupported format " + a;
  const st = o.strategy || "none";
  if (!["none", "lossless", "downsample"].includes(st)) return "optimize.strategy must be none, lossless or downsample";
  if (st === "downsample") {
    if (!o.imageDpi || o.imageDpi < 36 || o.imageDpi > 600) return "optimize.imageDpi must be 36–600";
    if (!o.jpegQuality || o.jpegQuality < 10 || o.jpegQuality > 100) return "optimize.jpegQuality must be 10–100";
  }
  if ((o.minSavingsPercent ?? 0) < 0 || (o.minSavingsPercent ?? 0) > 90) return "optimize.minSavingsPercent must be 0–90";
  if (th.enabled) {
    if (!th.maxSide || th.maxSide < 96 || th.maxSide > 2048) return "thumbnail.maxSide must be 96–2048";
    if (!th.maxBytes || th.maxBytes < 1024 || th.maxBytes > 1 << 20) return "thumbnail.maxBytes must be 1 KB–1 MB";
  }
  return "";
}
function validateVideo(p: Record<string, unknown>): string {
  const v = (p.video ?? {}) as { maxWidth?: number; fps?: number; crf?: number; maxrate?: string; bufsize?: string; preset?: string; hw?: string };
  const a = (p.audio ?? {}) as { bitrate?: string; channels?: number };
  const rate = /^[1-9][0-9]{1,5}[kKmM]$/;
  if (!v.maxWidth || v.maxWidth < 160 || v.maxWidth > 4096) return "video.maxWidth must be 160–4096";
  if (!v.fps || v.fps < 1 || v.fps > 60) return "video.fps must be 1–60";
  if (!v.crf || v.crf < 14 || v.crf > 40) return "video.crf must be 14–40";
  if (v.maxrate && !rate.test(v.maxrate)) return 'video.maxrate must look like "900k" or "4M"';
  if (v.bufsize && !rate.test(v.bufsize)) return 'video.bufsize must look like "1800k" or "8M"';
  if (v.hw && !["auto", "vaapi", "off"].includes(v.hw)) return "video.hw must be auto, vaapi or off";
  if (a.bitrate && !rate.test(a.bitrate)) return 'audio.bitrate must look like "96k"';
  if (a.channels !== undefined && (a.channels < 0 || a.channels > 2)) return "audio.channels must be 1 or 2";
  return "";
}

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
on("POST", "/signing-keys/publish", () => {
  if (sessionStorage.getItem("kadr-mock-edge") !== "1" /* set to "1" in devtools to simulate a configured edge */) {
    throw new HttpError(503, "edge_not_configured", "Service Unavailable", "set KADR_CF_ACCOUNT_ID and KADR_CF_API_TOKEN to publish keys to kadr-edge");
  }
  const live = mockSigning.filter((k) => k.status !== "retired");
  for (const k of live) k.published_at = nowIso();
  return json(200, { published: live.length });
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
  const actorType = query.get("actor_type");
  const actorId = query.get("actor_id");
  const all = [...mockAgentAudit, ...db.audit].filter(
    (e) => (!actorType || e.actor_type === actorType) && (!actorId || e.actor_id === actorId),
  );
  const items = all.slice(start, start + limit);
  const next = start + limit < all.length ? String(start + limit) : null;
  return json(200, { items, next_cursor: next });
});

// ---- AI agents --------------------------------------------------------------

const LEVEL_PERMS: Record<AgentLevel, string[]> = {
  read: ["analytics:read", "assets:read", "audit:read", "jobs:read", "keys:read", "projects:read", "storage:read", "system:read", "webhooks:read"],
  edit: [],
  full: [],
};
LEVEL_PERMS.edit = [...LEVEL_PERMS.read, "jobs:write", "moderation:write", "presets:write", "projects:write", "webhooks:write"].sort();
LEVEL_PERMS.full = [...LEVEL_PERMS.edit, "assets:delete", "keys:revoke", "webhooks:delete"].sort();

let agentsEnabled = true;
const mockAgents: Agent[] = [];
const mockApprovals: AgentApproval[] = [];
const mockAgentAudit: AuditEntry[] = [];

function agentPerms(level: AgentLevel, extra: string[]) {
  return [...LEVEL_PERMS[level], ...(level === "read" ? [] : extra)].sort();
}

function mcpSetup(token: string) {
  const url = `${window.location.origin}/mcp`;
  return {
    url,
    claude_code_command: `claude mcp add --transport http kadr ${url} --header "Authorization: Bearer ${token}"`,
    json_config: JSON.stringify({ mcpServers: { kadr: { type: "http", url, headers: { Authorization: `Bearer ${token}` } } } }, null, 2),
  };
}

function applyAgent(a: Agent, b: AgentInput, create: boolean) {
  const errors: Record<string, string> = {};
  if (b.name !== undefined || create) {
    if (!b.name?.trim()) errors.name = "required, at most 100 characters";
    else a.name = b.name.trim();
  }
  if (b.description !== undefined) a.description = b.description;
  if (b.level !== undefined) a.level = b.level;
  if (b.extra_scopes !== undefined) a.extra_scopes = b.extra_scopes;
  if (a.level === "read" && a.extra_scopes.length) errors.extra_scopes = "sensitive permissions need the edit or full level";
  if (b.project_ids !== undefined) {
    if (b.project_ids !== null && b.project_ids.length === 0) errors.project_ids = "list at least one project, or null for all projects";
    a.project_ids = b.project_ids;
  }
  if (b.approval_mode !== undefined) a.approval_mode = b.approval_mode;
  if (b.allowed_ips !== undefined) a.allowed_ips = b.allowed_ips;
  if (b.max_deletes_per_hour !== undefined) a.max_deletes_per_hour = b.max_deletes_per_hour;
  if (b.expires_in_days !== undefined) {
    a.expires_at = b.expires_in_days === 0 ? null : new Date(Date.now() + b.expires_in_days * 86400_000).toISOString();
  }
  if (Object.keys(errors).length) throw validationError(errors);
  a.permissions = agentPerms(a.level, a.extra_scopes);
  a.updated_at = nowIso();
}

function seedAgents() {
  if (mockAgents.length || !db.projects.length) return;
  const p = db.projects[0]!;
  const a: Agent = {
    id: ulid("agt_"), name: "claude-moderator", description: "Reviews new uploads against the content policy", prefix: `kd_agent_${randomBase62(8)}`,
    level: "edit", extra_scopes: [], permissions: agentPerms("edit", []), project_ids: [p.id], approval_mode: "destructive",
    allowed_ips: [], max_deletes_per_hour: 50, status: "active", created_by: db.admin.id, oauth_client_id: null, last_used_at: new Date(Date.now() - 600_000).toISOString(),
    last_used_ip: "203.0.113.7", expires_at: new Date(Date.now() + 80 * 86400_000).toISOString(), revoked_at: null, created_at: nowIso(), updated_at: nowIso(),
  };
  mockAgents.push(a);
  mockApprovals.push({
    id: ulid("apr_"), agent_id: a.id, agent_name: a.name, project_id: p.id, action: "assets:delete", tool: "kadr_delete_asset",
    method: "DELETE", path: `/projects/${p.id}/assets/ast_01JSPAM00000000000000000`, body: null,
    reason: "Spam advertisement overlay, rejected by moderation policy", status: "pending", result_status: null, result: null,
    decided_by: null, decision_note: null, created_at: nowIso(), expires_at: new Date(Date.now() + 86400_000).toISOString(), decided_at: null,
  });
  mockAgentAudit.push({
    id: "aud_agent1", actor_type: "agent", actor_id: a.id, project_id: p.id, action: "asset.moderate", target: "ast_01JDOC000000000000000000AB",
    details: { status: "flagged", labels: ["pii"], reason: "Document shows a passport number", tool: "kadr_set_moderation" }, ip: "203.0.113.7", created_at: nowIso(),
  });
}

function findAgent(id: string) {
  const a = mockAgents.find((x) => x.id === id);
  if (!a) throw new HttpError(404, "not_found", "Not found", "agent not found");
  return a;
}

on("GET", "/agents", () => {
  seedAgents();
  return json(200, { items: mockAgents });
});
on("GET", "/agents/settings", () => {
  seedAgents();
  return json(200, {
    enabled: agentsEnabled,
    pending_approvals: mockApprovals.filter((a) => a.status === "pending").length,
    mcp_url: `${window.location.origin}/mcp`,
    levels: LEVEL_PERMS,
    sensitive_scopes: ["keys:issue", "storage:manage", "edge:publish", "billing:manage", "cloud:connect"],
  });
});
on("PUT", "/agents/settings", ({ body }) => {
  agentsEnabled = (body as { enabled: boolean }).enabled;
  audit(agentsEnabled ? "agents.enable" : "agents.disable", null);
  persist();
  return json(200, {
    enabled: agentsEnabled,
    pending_approvals: mockApprovals.filter((a) => a.status === "pending").length,
    mcp_url: `${window.location.origin}/mcp`,
    levels: LEVEL_PERMS,
    sensitive_scopes: ["keys:issue", "storage:manage", "edge:publish", "billing:manage", "cloud:connect"],
  });
});
on("POST", "/agents", ({ body }) => {
  const token = `kd_agent_${randomBase62(8)}_${randomBase62(32)}`;
  const a: Agent = {
    id: ulid("agt_"), name: "", description: "", prefix: token.slice(0, 17), level: "read", extra_scopes: [], permissions: [],
    project_ids: null, approval_mode: "destructive", allowed_ips: [], max_deletes_per_hour: 50, status: "active", created_by: db.admin.id, oauth_client_id: null,
    last_used_at: null, last_used_ip: null, expires_at: null, revoked_at: null, created_at: nowIso(), updated_at: nowIso(),
  };
  const b = body as AgentInput;
  applyAgent(a, { expires_in_days: 90, ...b }, true);
  mockAgents.unshift(a);
  audit("agent.create", a.id, null, { name: a.name, level: a.level });
  persist();
  return json(201, { agent: a, token, mcp: mcpSetup(token) });
});
on("GET", "/agents/:id", ({ params }) => json(200, findAgent(params[0]!)));
on("PATCH", "/agents/:id", ({ params, body }) => {
  const a = findAgent(params[0]!);
  if (a.revoked_at) throw new HttpError(409, "revoked", "Conflict", "a revoked agent token cannot be changed");
  applyAgent(a, body as AgentInput, false);
  audit("agent.update", a.id);
  persist();
  return json(200, a);
});
on("POST", "/agents/:id/rotate", ({ params }) => {
  const a = findAgent(params[0]!);
  const token = `kd_agent_${randomBase62(8)}_${randomBase62(32)}`;
  a.prefix = token.slice(0, 17);
  audit("agent.rotate", a.id);
  persist();
  return json(200, { agent: a, token, mcp: mcpSetup(token) });
});
on("DELETE", "/agents/:id", ({ params }) => {
  const a = findAgent(params[0]!);
  a.revoked_at = nowIso();
  a.status = "revoked";
  for (const x of mockApprovals) if (x.agent_id === a.id && x.status === "pending") x.status = "rejected";
  audit("agent.revoke", a.id);
  persist();
  return noContent();
});
on("GET", "/agent-approvals", ({ query }) => {
  const st = query.get("status");
  return json(200, { items: mockApprovals.filter((a) => !st || a.status === st) });
});
on("GET", "/agent-approvals/:id", ({ params }) => {
  const a = mockApprovals.find((x) => x.id === params[0]);
  if (!a) throw new HttpError(404, "not_found", "Not found", "approval not found");
  return json(200, a);
});
const decideApproval = (approve: boolean) => (ctx: { params: string[]; body: unknown }) => {
  const a = mockApprovals.find((x) => x.id === ctx.params[0]);
  if (!a) throw new HttpError(404, "not_found", "Not found", "approval not found");
  if (a.status !== "pending") throw new HttpError(409, "not_pending", "Conflict", "the request was already decided or has expired");
  const note = (ctx.body as { note?: string } | undefined)?.note;
  a.status = approve ? "executed" : "rejected";
  a.decided_by = db.admin.id;
  a.decided_at = nowIso();
  a.decision_note = note || null;
  if (approve) a.result_status = 204;
  audit(approve ? "agent.approval.approve" : "agent.approval.reject", a.id, a.project_id);
  persist();
  return json(200, a);
};
on("POST", "/agent-approvals/:id/approve", decideApproval(true));
on("POST", "/agent-approvals/:id/reject", decideApproval(false));

// ---- Moderation ---------------------------------------------------------------

const mockModeration = new Map<string, Moderation>();
const MOCK_COLORS = ["#f59e0b", "#6366f1", "#10b981", "#ef4444", "#0ea5e9", "#a855f7"];
function moderationItems(projectId: string): ModerationItem[] {
  return Array.from({ length: 10 }, (_, i) => {
    const id = `ast_01JMOD${String(i).padStart(2, "0")}${projectId.slice(-12).toUpperCase()}`.slice(0, 30);
    return {
      asset_id: id,
      kind: i % 4 === 3 ? "video" : "image",
      namespace: `user_${100 + i}`,
      visibility: i % 3 === 0 ? "private" : "public",
      created_at: new Date(Date.now() - i * 3_600_000).toISOString(),
      moderation: mockModeration.get(id) ?? { asset_id: id, status: "unreviewed", labels: [], note: null, actor_type: null, actor_id: null, updated_at: null },
    };
  });
}
on("GET", "/projects/:id/moderation", ({ params, query }) => {
  findProject(params[0]!);
  const st = query.get("status") || "unreviewed";
  const kind = query.get("kind");
  const items = moderationItems(params[0]!).filter((it) => (st === "all" || it.moderation.status === st) && (!kind || it.kind === kind));
  return json(200, { items, next_cursor: null });
});
on("PUT", "/projects/:id/assets/:aid/moderation", ({ params, body }) => {
  findProject(params[0]!);
  const b = body as SetModerationRequest;
  if (!["approved", "flagged", "rejected"].includes(b.status)) throw validationError({ status: "must be approved, flagged or rejected" });
  const m: Moderation = { asset_id: params[1]!, status: b.status, labels: b.labels ?? [], note: b.note ?? null, actor_type: "admin", actor_id: db.admin.id, updated_at: nowIso() };
  mockModeration.set(params[1]!, m);
  audit("asset.moderate", params[1]!, params[0]!, { status: b.status, labels: m.labels });
  persist();
  return json(200, m);
});
on("GET", "/projects/:id/assets/:aid/preview", ({ params }) => {
  const n = Number(params[1]!.slice(8, 10)) || 0;
  const color = MOCK_COLORS[n % MOCK_COLORS.length]!;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="480"><rect width="100%" height="100%" fill="${color}"/><circle cx="240" cy="200" r="90" fill="#ffffff55"/><rect x="90" y="330" width="300" height="40" rx="8" fill="#ffffff44"/></svg>`;
  return new Response(svg, { status: 200, headers: { "Content-Type": "image/svg+xml" } });
});

// ---- AI operations ------------------------------------------------------------

const MOCK_AI = { available: true, classifier: "claude:claude-opus-5-5", agent_runs: true, model: "claude-opus-5-5" };
const mockModerationSettings = new Map<string, ModerationSettings>();
const DEFAULT_POLICY =
  "No sexual content or nudity, no graphic violence or gore, no hate symbols, no visible personal data (ID documents, bank cards, addresses), no spam or advertising overlays.";

on("GET", "/ai/status", () => json(200, MOCK_AI));
on("GET", "/projects/:id/moderation/settings", ({ params }) => {
  findProject(params[0]!);
  const st = mockModerationSettings.get(params[0]!) ?? { enabled: false, policy: DEFAULT_POLICY, mode: "flag_only", kinds: ["image", "video"], since: "" };
  return json(200, { settings: st, ai: MOCK_AI });
});
on("PUT", "/projects/:id/moderation/settings", ({ params, body }) => {
  findProject(params[0]!);
  const b = body as ModerationSettings;
  const prev = mockModerationSettings.get(params[0]!);
  const st: ModerationSettings = {
    enabled: !!b.enabled,
    policy: b.policy?.trim() || DEFAULT_POLICY,
    mode: b.mode === "decide" ? "decide" : "flag_only",
    kinds: b.kinds?.length ? b.kinds : ["image", "video"],
    since: b.enabled ? (prev?.enabled && prev.since ? prev.since : nowIso()) : (prev?.since ?? ""),
  };
  mockModerationSettings.set(params[0]!, st);
  audit("moderation.settings", params[0]!, params[0]!, { enabled: st.enabled, mode: st.mode });
  return json(200, { settings: st, ai: MOCK_AI });
});
on("POST", "/projects/:id/moderation/backlog", ({ params }) => {
  findProject(params[0]!);
  let n = 0;
  for (const it of moderationItems(params[0]!)) {
    if (it.moderation.status !== "unreviewed") continue;
    n++;
    const flagged = n % 3 === 0;
    mockModeration.set(it.asset_id, {
      asset_id: it.asset_id, status: flagged ? "flagged" : "approved", labels: flagged ? ["nsfw"] : [],
      note: flagged ? "Partly visible nudity; needs a human look." : null, actor_type: "system", actor_id: MOCK_AI.classifier, updated_at: nowIso(),
    });
  }
  return json(202, { queued: n });
});

const mockSchedules: AgentSchedule[] = [];
const mockRuns: AgentRun[] = [];

function nextRunIso(s: Pick<AgentSchedule, "frequency" | "time_utc" | "weekday">): string {
  const now = new Date();
  const [h, m] = s.time_utc.split(":").map(Number) as [number, number];
  const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), s.frequency === "hourly" ? now.getUTCHours() : h, m));
  if (s.frequency === "weekly") d.setUTCDate(d.getUTCDate() + ((s.weekday - d.getUTCDay() + 7) % 7));
  while (d <= now) d.setTime(d.getTime() + (s.frequency === "hourly" ? 3600_000 : s.frequency === "weekly" ? 7 * 86400_000 : 86400_000));
  return d.toISOString();
}

function seedSchedules() {
  seedAgents();
  if (mockSchedules.length || !mockAgents.length) return;
  const a = mockAgents[0]!;
  const s: AgentSchedule = {
    id: ulid("ags_"), name: "Nightly moderation", agent_id: a.id, agent_name: a.name,
    prompt: "Review unreviewed assets of every project against the policy; approve clear cases, flag doubtful ones; never delete. Report counts per project.",
    frequency: "daily", time_utc: "02:30", weekday: 1, max_turns: 30, notify: true, enabled: true,
    last_run_at: new Date(Date.now() - 20 * 3600_000).toISOString(), next_run_at: null, created_at: nowIso(),
  };
  s.next_run_at = nextRunIso(s);
  mockSchedules.push(s);
  mockRuns.push({
    id: ulid("agr_"), schedule_id: s.id, agent_id: a.id, agent_name: a.name, trigger: "schedule", prompt: s.prompt, status: "succeeded",
    output: "## Nightly moderation\n\n| Project | Approved | Flagged |\n|---|---|---|\n| Acme | 41 | 3 |\n\nFlagged items need a human look (labels: nsfw ×2, pii ×1). Nothing was deleted.",
    error: null, tool_calls: 52, input_tokens: 184_220, output_tokens: 6_310, model: MOCK_AI.model,
    created_at: s.last_run_at!, started_at: s.last_run_at, finished_at: new Date(Date.parse(s.last_run_at!) + 240_000).toISOString(),
  });
}

function findSchedule(id: string) {
  const s = mockSchedules.find((x) => x.id === id);
  if (!s) throw new HttpError(404, "not_found", "Not found", "schedule not found");
  return s;
}

function startMockRun(agentId: string, prompt: string, scheduleId: string | null): AgentRun {
  const a = mockAgents.find((x) => x.id === agentId);
  const run: AgentRun = {
    id: ulid("agr_"), schedule_id: scheduleId, agent_id: agentId, agent_name: a?.name ?? agentId, trigger: "manual", prompt, status: "queued",
    output: null, error: null, tool_calls: 0, input_tokens: 0, output_tokens: 0, model: MOCK_AI.model, created_at: nowIso(), started_at: null, finished_at: null,
  };
  mockRuns.unshift(run);
  setTimeout(() => {
    run.status = "running";
    run.started_at = nowIso();
  }, 1500);
  setTimeout(() => {
    run.status = "succeeded";
    run.finished_at = nowIso();
    run.tool_calls = 7;
    run.input_tokens = 21_400;
    run.output_tokens = 900;
    run.output = "## Report\n\nAll projects healthy. No failed jobs in the last 24 hours; webhook deliveries 100% successful.";
  }, 8000);
  return run;
}

function applySchedule(s: AgentSchedule, b: ScheduleInput) {
  const errors: Record<string, string> = {};
  if (b.name !== undefined) s.name = b.name.trim();
  if (!s.name) errors.name = "required, at most 100 characters";
  if (b.agent_id !== undefined) s.agent_id = b.agent_id;
  const a = mockAgents.find((x) => x.id === s.agent_id);
  if (!a || a.status === "revoked") errors.agent_id = "choose an active agent token; the run gets exactly its permissions";
  else s.agent_name = a.name;
  if (b.prompt !== undefined) s.prompt = b.prompt.trim();
  if (!s.prompt) errors.prompt = "required, at most 20000 characters";
  if (b.frequency !== undefined) s.frequency = b.frequency;
  if (b.time_utc !== undefined) s.time_utc = b.time_utc;
  if (!/^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(s.time_utc)) errors.time_utc = "HH:MM (UTC)";
  if (b.weekday !== undefined) s.weekday = b.weekday;
  if (b.max_turns !== undefined) s.max_turns = b.max_turns;
  if (b.notify !== undefined) s.notify = b.notify;
  if (b.enabled !== undefined) s.enabled = b.enabled;
  if (Object.keys(errors).length) throw validationError(errors);
  s.next_run_at = s.enabled ? nextRunIso(s) : null;
}

on("GET", "/agent-schedules", () => {
  seedSchedules();
  return json(200, { items: mockSchedules, ai: MOCK_AI });
});
on("POST", "/agent-schedules", ({ body }) => {
  seedSchedules();
  const s: AgentSchedule = {
    id: ulid("ags_"), name: "", agent_id: "", agent_name: "", prompt: "", frequency: "daily", time_utc: "03:00", weekday: 1, max_turns: 30,
    notify: true, enabled: true, last_run_at: null, next_run_at: null, created_at: nowIso(),
  };
  applySchedule(s, body as ScheduleInput);
  mockSchedules.unshift(s);
  audit("agent.schedule.create", s.id, null, { name: s.name });
  return json(201, s);
});
on("PATCH", "/agent-schedules/:id", ({ params, body }) => {
  const s = findSchedule(params[0]!);
  applySchedule(s, body as ScheduleInput);
  audit("agent.schedule.update", s.id);
  return json(200, s);
});
on("DELETE", "/agent-schedules/:id", ({ params }) => {
  const s = findSchedule(params[0]!);
  mockSchedules.splice(mockSchedules.indexOf(s), 1);
  audit("agent.schedule.delete", s.id);
  return noContent();
});
on("POST", "/agent-schedules/:id/run", ({ params }) => {
  const s = findSchedule(params[0]!);
  return json(202, startMockRun(s.agent_id, s.prompt, s.id));
});
on("GET", "/agent-runs", ({ query }) => {
  seedSchedules();
  const sid = query.get("schedule_id");
  return json(200, { items: mockRuns.filter((r) => !sid || r.schedule_id === sid) });
});
on("GET", "/agent-runs/:id", ({ params }) => {
  const r = mockRuns.find((x) => x.id === params[0]);
  if (!r) throw new HttpError(404, "not_found", "Not found", "run not found");
  return json(200, r);
});
on("POST", "/agent-runs", ({ body }) => {
  seedAgents();
  const b = body as { agent_id: string; prompt: string };
  const errors: Record<string, string> = {};
  if (!b.prompt?.trim()) errors.prompt = "required, at most 20000 characters";
  if (!mockAgents.some((a) => a.id === b.agent_id && a.status === "active")) errors.agent_id = "choose an active agent token";
  if (Object.keys(errors).length) throw validationError(errors);
  return json(202, startMockRun(b.agent_id, b.prompt.trim(), null));
});

// ---- OAuth consent --------------------------------------------------------------

const mockGrants = new Map<string, "pending" | "approved" | "denied">();
on("GET", "/oauth/grants/:id", ({ params }) => {
  const st = mockGrants.get(params[0]!) ?? "pending";
  if (st !== "pending") throw new HttpError(409, "not_pending", "Conflict", "this authorization request was already answered or has expired");
  return json(200, {
    id: params[0]!, client_id: "oac_01JMOCKCLAUDE0000000000000", client_name: "Claude", redirect_host: "claude.ai",
    redirect_uri: "https://claude.ai/api/mcp/auth_callback", scope: "kadr:edit", suggested_level: "edit",
    client_registered_at: new Date(Date.now() - 60_000).toISOString(), expires_at: new Date(Date.now() + 9 * 60_000).toISOString(),
  });
});
on("POST", "/oauth/grants/:id/approve", ({ params, body }) => {
  if ((mockGrants.get(params[0]!) ?? "pending") !== "pending") throw new HttpError(409, "not_pending", "Conflict", "already answered");
  mockGrants.set(params[0]!, "approved");
  const b = body as AgentInput;
  const a: Agent = {
    id: ulid("agt_"), name: "", description: "", prefix: `kd_agent_${randomBase62(8)}`, level: "read", extra_scopes: [], permissions: [],
    project_ids: null, approval_mode: "destructive", allowed_ips: [], max_deletes_per_hour: 50, status: "active", created_by: db.admin.id,
    oauth_client_id: "oac_01JMOCKCLAUDE0000000000000", last_used_at: null, last_used_ip: null, expires_at: null, revoked_at: null,
    created_at: nowIso(), updated_at: nowIso(),
  };
  applyAgent(a, { expires_in_days: 90, ...b }, true);
  mockAgents.unshift(a);
  audit("agent.oauth.approve", a.id, null, { client_name: "Claude" });
  persist();
  // The mock cannot leave the app: return to the agents list instead of claude.ai.
  return json(200, { redirect_url: "/agents", agent: a });
});
on("POST", "/oauth/grants/:id/deny", ({ params }) => {
  mockGrants.set(params[0]!, "denied");
  return json(200, { redirect_url: "/agents" });
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
