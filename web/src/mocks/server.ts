// In-memory implementation of docs/ADMIN_API.md for `VITE_MOCK=1 pnpm dev`.
// It patches window.fetch for /admin/api/* requests only. This module is loaded
// via a dynamic import guarded by import.meta.env.VITE_MOCK, so it is never part
// of a production build.
import type {
  ApiKey,
  ApiKeyScope,
  CreateApiKeyRequest,
  CreateProjectRequest,
  LoginRequest,
  Project,
  StorageProfile,
  StorageProfileInput,
  StorageTestResult,
  SystemInfo,
  UpdateProjectRequest,
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
    const body = text ? (JSON.parse(text) as unknown) : undefined;
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
