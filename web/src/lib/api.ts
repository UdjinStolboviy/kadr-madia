import type {
  ApiErrorCode,
  ApiKey,
  AuditPage,
  CreateApiKeyRequest,
  CreateApiKeyResponse,
  CreateProjectRequest,
  ItemsResponse,
  LoginRequest,
  Preset,
  Problem,
  Project,
  SessionResponse,
  StorageProfile,
  StorageProfileInput,
  StorageProfilePatch,
  StorageTestResult,
  SystemInfo,
  TotpSetupResponse,
  UpdateProjectRequest,
} from "./types";

export const API_BASE = "/admin/api";

/** Typed error parsed from an `application/problem+json` response. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode;
  readonly title: string;
  readonly detail?: string;
  readonly fieldErrors: Record<string, string>;

  constructor(problem: Problem) {
    super(problem.detail || problem.title || problem.code);
    this.name = "ApiError";
    this.status = problem.status;
    this.code = problem.code;
    this.title = problem.title;
    this.detail = problem.detail;
    this.fieldErrors = problem.errors ?? {};
  }

  is(code: ApiErrorCode): boolean {
    return this.code === code;
  }
}

export function isApiError(e: unknown, code?: ApiErrorCode): e is ApiError {
  return e instanceof ApiError && (code === undefined || e.code === code);
}

/** Human-friendly message for any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "rate_limited":
        return "Too many attempts. Please wait a minute and try again.";
      case "csrf_failed":
        return "Your session token expired. Reload the page and try again.";
      case "network_error":
        return "Can't reach the Kadr server. Check that it is running.";
    }
    return e.detail || e.title || e.code;
  }
  if (e instanceof Error) return e.message;
  return "Something went wrong";
}

// ---- CSRF token (memory only) ----------------------------------------------

let csrfToken: string | null = null;

export function setCsrfToken(token: string | null) {
  csrfToken = token;
}

// ---- Global 401 handling ----------------------------------------------------

type UnauthorizedHandler = () => void;
let onUnauthorized: UnauthorizedHandler | null = null;

/** Registered by the app to redirect to /login when a session expires. */
export function setUnauthorizedHandler(fn: UnauthorizedHandler | null) {
  onUnauthorized = fn;
}

// ---- Transport --------------------------------------------------------------

const MUTATING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  query?: Record<string, string | number | undefined | null>;
  /** Do not trigger global unauthenticated redirect (login, me). */
  skipAuthRedirect?: boolean;
  signal?: AbortSignal;
}

async function parseProblem(res: Response): Promise<Problem> {
  const fallback: Problem = {
    type: "about:blank",
    title: res.statusText || "Request failed",
    status: res.status,
    code: res.status === 401 ? "unauthenticated" : res.status === 429 ? "rate_limited" : `http_${res.status}`,
  };
  const ct = res.headers.get("content-type") ?? "";
  if (!ct.includes("json")) return fallback;
  try {
    const body = (await res.json()) as Partial<Problem>;
    return { ...fallback, ...body, status: body.status ?? res.status, code: body.code ?? fallback.code };
  } catch {
    return fallback;
  }
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  let url = API_BASE + path;
  if (opts.query) {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(opts.query)) {
      if (v !== undefined && v !== null && v !== "") qs.set(k, String(v));
    }
    const s = qs.toString();
    if (s) url += `?${s}`;
  }

  const headers: Record<string, string> = { Accept: "application/json" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (MUTATING.has(method) && path !== "/auth/login" && csrfToken) {
    headers["X-CSRF-Token"] = csrfToken;
  }

  let res: Response;
  try {
    res = await fetch(url, {
      method,
      headers,
      credentials: "same-origin",
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
      signal: opts.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError({
      type: "about:blank",
      title: "Network error",
      status: 0,
      code: "network_error",
      detail: "Can't reach the Kadr server.",
    });
  }

  if (!res.ok) {
    const problem = await parseProblem(res);
    const err = new ApiError(problem);
    if (err.status === 401 && err.code === "unauthenticated" && !opts.skipAuthRedirect) {
      csrfToken = null;
      onUnauthorized?.();
    }
    throw err;
  }

  if (res.status === 204) return undefined as T;
  const ct = res.headers.get("content-type") ?? "";
  if (!ct.includes("json")) return undefined as T;
  return (await res.json()) as T;
}

// ---- Endpoints --------------------------------------------------------------

const enc = encodeURIComponent;

export const api = {
  auth: {
    async login(body: LoginRequest): Promise<SessionResponse> {
      const res = await request<SessionResponse>("/auth/login", { method: "POST", body, skipAuthRedirect: true });
      setCsrfToken(res.csrf_token);
      return res;
    },
    async logout(): Promise<void> {
      try {
        await request<void>("/auth/logout", { method: "POST", skipAuthRedirect: true });
      } finally {
        setCsrfToken(null);
      }
    },
    async me(): Promise<SessionResponse> {
      const res = await request<SessionResponse>("/auth/me", { skipAuthRedirect: true });
      setCsrfToken(res.csrf_token);
      return res;
    },
    totpSetup: () => request<TotpSetupResponse>("/auth/totp/setup", { method: "POST" }),
    totpEnable: (code: string) => request<void>("/auth/totp/enable", { method: "POST", body: { code } }),
    totpDisable: (password: string) => request<void>("/auth/totp/disable", { method: "POST", body: { password } }),
  },

  projects: {
    list: () => request<ItemsResponse<Project>>("/projects"),
    get: (id: string) => request<Project>(`/projects/${enc(id)}`),
    create: (body: CreateProjectRequest) => request<Project>("/projects", { method: "POST", body }),
    update: (id: string, body: UpdateProjectRequest) =>
      request<Project>(`/projects/${enc(id)}`, { method: "PATCH", body }),
    presets: (id: string) => request<ItemsResponse<Preset>>(`/projects/${enc(id)}/presets`),
  },

  apiKeys: {
    list: (projectId: string) => request<ItemsResponse<ApiKey>>(`/projects/${enc(projectId)}/api-keys`),
    create: (projectId: string, body: CreateApiKeyRequest) =>
      request<CreateApiKeyResponse>(`/projects/${enc(projectId)}/api-keys`, { method: "POST", body }),
    revoke: (keyId: string) => request<void>(`/api-keys/${enc(keyId)}`, { method: "DELETE" }),
  },

  storage: {
    list: () => request<ItemsResponse<StorageProfile>>("/storage-profiles"),
    get: (id: string) => request<StorageProfile>(`/storage-profiles/${enc(id)}`),
    create: (body: StorageProfileInput) => request<StorageProfile>("/storage-profiles", { method: "POST", body }),
    update: (id: string, body: StorageProfilePatch) =>
      request<StorageProfile>(`/storage-profiles/${enc(id)}`, { method: "PATCH", body }),
    remove: (id: string) => request<void>(`/storage-profiles/${enc(id)}`, { method: "DELETE" }),
    test: (id: string) => request<StorageTestResult>(`/storage-profiles/${enc(id)}/test`, { method: "POST" }),
  },

  system: {
    get: () => request<SystemInfo>("/system"),
  },

  audit: {
    list: (params: { limit?: number; cursor?: string | null }) =>
      request<AuditPage>("/audit", { query: { limit: params.limit ?? 50, cursor: params.cursor } }),
  },
};
