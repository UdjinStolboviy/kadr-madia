import { t } from "@/i18n/store";

import type {
  Agent,
  AgentApproval,
  AgentRun,
  AgentSchedule,
  AgentInput,
  AgentSecretResponse,
  AgentSettings,
  AuditFilters,
  Moderation,
  ModerationPage,
  ModerationSettings,
  ModerationSettingsResponse,
  OAuthApproveResponse,
  OAuthGrant,
  ScheduleInput,
  SchedulesResponse,
  SetModerationRequest,
  Analytics,
  ApiErrorCode,
  ApiKey,
  Asset,
  AssetFilters,
  AssetPage,
  AuditPage,
  CloudConnection,
  CreateApiKeyRequest,
  CreateApiKeyResponse,
  CreateCloudConnectionRequest,
  CreateProjectRequest,
  CreateSigningKeyResponse,
  CreateWebhookRequest,
  CreateWebhookResponse,
  Delivery,
  DeliveryPage,
  ItemsResponse,
  Job,
  JobFilters,
  ModesResponse,
  LoginRequest,
  PlaygroundResult,
  Preset,
  Problem,
  Project,
  SavePresetVersionRequest,
  SessionResponse,
  SigningKey,
  StorageProfile,
  StorageProfileInput,
  StorageProfilePatch,
  StorageTestResult,
  SystemInfo,
  TotpSetupResponse,
  Passkey,
  WebAuthnCeremony,
  UpdateProjectRequest,
  WebhookEndpoint,
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

/** Human-friendly, translated message for any thrown value. Known codes map to client-side text. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "rate_limited":
        return t("shell.err.rateLimited");
      case "csrf_failed":
        return t("shell.err.csrfFailed");
      case "network_error":
        return t("shell.err.network");
      case "unauthenticated":
        return t("shell.err.unauthenticated");
      case "invalid_credentials":
        return t("shell.err.invalidCredentials");
      case "invalid_code":
        return t("shell.err.invalidCode");
      case "not_found":
        return t("shell.err.notFound");
      case "slug_taken":
        return t("shell.err.slugTaken");
      case "in_use":
        return t("shell.err.inUse");
      case "webauthn_disabled":
        return t("shell.err.webauthnDisabled");
    }
    // validation_failed and unknown codes: the server's (English) detail is more specific.
    return e.detail || e.title || e.code;
  }
  if (e instanceof Error) return e.message;
  return t("shell.err.generic");
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
  /** Multipart upload. Content-Type is left to the browser (boundary); the CSRF header is still sent. */
  form?: FormData;
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
  if (opts.body !== undefined && !opts.form) headers["Content-Type"] = "application/json";
  if (MUTATING.has(method) && path !== "/auth/login" && csrfToken) {
    headers["X-CSRF-Token"] = csrfToken;
  }

  let res: Response;
  try {
    res = await fetch(url, {
      method,
      headers,
      credentials: "same-origin",
      body: opts.form ?? (opts.body !== undefined ? JSON.stringify(opts.body) : undefined),
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
    passkeys: () => request<{ data: Passkey[] }>("/auth/webauthn/credentials"),
    passkeyRegisterBegin: () => request<WebAuthnCeremony>("/auth/webauthn/register/begin", { method: "POST" }),
    passkeyRegisterFinish: (body: { ceremony_id: string; name: string; credential: unknown }) =>
      request<Passkey>("/auth/webauthn/register/finish", { method: "POST", body }),
    passkeyDelete: (id: string) => request<void>(`/auth/webauthn/credentials/${enc(id)}`, { method: "DELETE" }),
    passkeyLoginBegin: () =>
      request<WebAuthnCeremony>("/auth/webauthn/login/begin", { method: "POST", skipAuthRedirect: true }),
    async passkeyLoginFinish(body: { ceremony_id: string; credential: unknown }): Promise<SessionResponse> {
      const res = await request<SessionResponse>("/auth/webauthn/login/finish", {
        method: "POST",
        body,
        skipAuthRedirect: true,
      });
      setCsrfToken(res.csrf_token);
      return res;
    },
  },

  projects: {
    list: () => request<ItemsResponse<Project>>("/projects"),
    get: (id: string) => request<Project>(`/projects/${enc(id)}`),
    create: (body: CreateProjectRequest) => request<Project>("/projects", { method: "POST", body }),
    update: (id: string, body: UpdateProjectRequest) =>
      request<Project>(`/projects/${enc(id)}`, { method: "PATCH", body }),
    presets: (id: string) => request<ItemsResponse<Preset>>(`/projects/${enc(id)}/presets`),
    savePresetVersion: (id: string, slug: string, body: SavePresetVersionRequest) =>
      request<Preset>(`/projects/${enc(id)}/presets/${enc(slug)}/versions`, { method: "POST", body }),
    modes: (id: string) => request<ModesResponse>(`/projects/${enc(id)}/modes`),
    resetPreset: (id: string, slug: string) =>
      request<Preset>(`/projects/${enc(id)}/presets/${enc(slug)}/reset`, { method: "POST" }),
    analytics: (id: string, days: number) => request<Analytics>(`/projects/${enc(id)}/analytics`, { query: { days } }),
  },

  jobs: {
    list: (f: JobFilters = {}) => request<ItemsResponse<Job>>("/jobs", { query: { ...f } }),
    retry: (id: string) => request<Job>(`/jobs/${enc(id)}/retry`, { method: "POST" }),
    cancel: (id: string) => request<Job>(`/jobs/${enc(id)}/cancel`, { method: "POST" }),
  },

  assets: {
    list: (projectId: string, f: AssetFilters & { cursor?: string | null; limit?: number }) =>
      request<AssetPage>(`/projects/${enc(projectId)}/assets`, { query: { ...f } }),
    get: (projectId: string, assetId: string) =>
      request<Asset>(`/projects/${enc(projectId)}/assets/${enc(assetId)}`),
    remove: (projectId: string, assetId: string) =>
      request<void>(`/projects/${enc(projectId)}/assets/${enc(assetId)}`, { method: "DELETE" }),
  },

  playground: {
    run: (file: File, params: unknown, signal?: AbortSignal) => {
      const form = new FormData();
      form.append("file", file);
      form.append("params", JSON.stringify(params));
      return request<PlaygroundResult>("/playground", { method: "POST", form, signal });
    },
  },

  webhooks: {
    list: (projectId: string) => request<ItemsResponse<WebhookEndpoint>>(`/projects/${enc(projectId)}/webhooks`),
    create: (projectId: string, body: CreateWebhookRequest) =>
      request<CreateWebhookResponse>(`/projects/${enc(projectId)}/webhooks`, { method: "POST", body }),
    remove: (id: string) => request<void>(`/webhooks/${enc(id)}`, { method: "DELETE" }),
    test: (id: string) => request<Delivery>(`/webhooks/${enc(id)}/test`, { method: "POST" }),
    deliveries: (projectId: string, p: { status?: string; cursor?: string | null; limit?: number }) =>
      request<DeliveryPage>(`/projects/${enc(projectId)}/deliveries`, { query: { ...p } }),
    retryDelivery: (id: string) => request<Delivery>(`/deliveries/${enc(id)}/retry`, { method: "POST" }),
  },

  signingKeys: {
    list: (projectId: string) => request<ItemsResponse<SigningKey>>(`/projects/${enc(projectId)}/signing-keys`),
    rotate: (projectId: string) =>
      request<CreateSigningKeyResponse>(`/projects/${enc(projectId)}/signing-keys`, { method: "POST" }),
    retire: (kid: string) => request<void>(`/signing-keys/${enc(kid)}`, { method: "DELETE" }),
    publish: () => request<{ published: number }>("/signing-keys/publish", { method: "POST" }),
  },

  cloud: {
    list: (projectId: string) => request<ItemsResponse<CloudConnection>>(`/projects/${enc(projectId)}/cloud-connections`),
    create: (projectId: string, body: CreateCloudConnectionRequest) =>
      request<CloudConnection>(`/projects/${enc(projectId)}/cloud-connections`, { method: "POST", body }),
    revoke: (id: string) => request<void>(`/cloud-connections/${enc(id)}`, { method: "DELETE" }),
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
    list: (params: { limit?: number; cursor?: string | null } & AuditFilters) =>
      request<AuditPage>("/audit", { query: { ...params, limit: params.limit ?? 50, cursor: params.cursor } }),
  },

  agents: {
    list: () => request<ItemsResponse<Agent>>("/agents"),
    get: (id: string) => request<Agent>(`/agents/${enc(id)}`),
    create: (body: AgentInput) => request<AgentSecretResponse>("/agents", { method: "POST", body }),
    update: (id: string, body: AgentInput) => request<Agent>(`/agents/${enc(id)}`, { method: "PATCH", body }),
    rotate: (id: string) => request<AgentSecretResponse>(`/agents/${enc(id)}/rotate`, { method: "POST" }),
    revoke: (id: string) => request<void>(`/agents/${enc(id)}`, { method: "DELETE" }),
    settings: () => request<AgentSettings>("/agents/settings"),
    setEnabled: (enabled: boolean) => request<AgentSettings>("/agents/settings", { method: "PUT", body: { enabled } }),
  },

  approvals: {
    list: (p: { status?: string; agent_id?: string; limit?: number } = {}) =>
      request<ItemsResponse<AgentApproval>>("/agent-approvals", { query: { ...p } }),
    get: (id: string) => request<AgentApproval>(`/agent-approvals/${enc(id)}`),
    approve: (id: string, note?: string) =>
      request<AgentApproval>(`/agent-approvals/${enc(id)}/approve`, { method: "POST", body: { note: note ?? "" } }),
    reject: (id: string, note?: string) =>
      request<AgentApproval>(`/agent-approvals/${enc(id)}/reject`, { method: "POST", body: { note: note ?? "" } }),
  },

  moderation: {
    queue: (projectId: string, p: { status?: string; kind?: string; cursor?: string | null; limit?: number }) =>
      request<ModerationPage>(`/projects/${enc(projectId)}/moderation`, { query: { ...p } }),
    set: (projectId: string, assetId: string, body: SetModerationRequest) =>
      request<Moderation>(`/projects/${enc(projectId)}/assets/${enc(assetId)}/moderation`, { method: "PUT", body }),
    previewUrl: (projectId: string, assetId: string, width = 480) =>
      `${API_BASE}/projects/${enc(projectId)}/assets/${enc(assetId)}/preview?width=${width}`,
    settings: (projectId: string) => request<ModerationSettingsResponse>(`/projects/${enc(projectId)}/moderation/settings`),
    setSettings: (projectId: string, body: Omit<ModerationSettings, "since">) =>
      request<ModerationSettingsResponse>(`/projects/${enc(projectId)}/moderation/settings`, { method: "PUT", body }),
    backlog: (projectId: string) => request<{ queued: number }>(`/projects/${enc(projectId)}/moderation/backlog`, { method: "POST" }),
  },

  oauth: {
    grant: (id: string) => request<OAuthGrant>(`/oauth/grants/${enc(id)}`),
    approve: (id: string, body: AgentInput) => request<OAuthApproveResponse>(`/oauth/grants/${enc(id)}/approve`, { method: "POST", body }),
    deny: (id: string) => request<{ redirect_url: string }>(`/oauth/grants/${enc(id)}/deny`, { method: "POST" }),
  },

  schedules: {
    list: () => request<SchedulesResponse>("/agent-schedules"),
    create: (body: ScheduleInput) => request<AgentSchedule>("/agent-schedules", { method: "POST", body }),
    update: (id: string, body: ScheduleInput) => request<AgentSchedule>(`/agent-schedules/${enc(id)}`, { method: "PATCH", body }),
    remove: (id: string) => request<void>(`/agent-schedules/${enc(id)}`, { method: "DELETE" }),
    run: (id: string) => request<AgentRun>(`/agent-schedules/${enc(id)}/run`, { method: "POST" }),
  },

  runs: {
    list: (p: { schedule_id?: string; limit?: number } = {}) => request<ItemsResponse<AgentRun>>("/agent-runs", { query: { ...p } }),
    get: (id: string) => request<AgentRun>(`/agent-runs/${enc(id)}`),
    create: (body: { agent_id: string; prompt: string }) => request<AgentRun>("/agent-runs", { method: "POST", body }),
  },
};
