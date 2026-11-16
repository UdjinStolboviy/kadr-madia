// Types mirroring docs/ADMIN_API.md (M0). Fields are snake_case, times are RFC 3339 UTC strings.

export type ISODateTime = string;

// ---- Errors -----------------------------------------------------------------

export type ApiErrorCode =
  | "unauthenticated"
  | "csrf_failed"
  | "invalid_credentials"
  | "totp_required"
  | "rate_limited"
  | "invalid_code"
  | "validation_failed"
  | "slug_taken"
  | "in_use"
  | "not_found"
  // Unknown/forward-compatible codes from the server, or client-side failures.
  | (string & {});

export interface Problem {
  type: string;
  title: string;
  status: number;
  code: ApiErrorCode;
  detail?: string;
  errors?: Record<string, string>;
}

// ---- Auth -------------------------------------------------------------------

export type AdminRole = string;

export interface Admin {
  id: string;
  email: string;
  role: AdminRole;
  totp_enabled: boolean;
  created_at: ISODateTime;
  last_login_at: ISODateTime | null;
}

export interface LoginRequest {
  email: string;
  password: string;
  totp_code?: string;
}

export interface SessionResponse {
  admin: Admin;
  csrf_token: string;
}

export interface TotpSetupResponse {
  secret: string;
  otpauth_url: string;
}

export interface TotpEnableRequest {
  code: string;
}

export interface TotpDisableRequest {
  password: string;
}

// ---- Lists ------------------------------------------------------------------

export interface ItemsResponse<T> {
  items: T[];
}

// ---- Projects ---------------------------------------------------------------

export type Environment = "live" | "test";

export interface Project {
  id: string;
  slug: string;
  name: string;
  environment: Environment;
  storage_profile_id: string | null;
  allowed_origins: string[];
  quota_bytes: number | null;
  used_bytes: number;
  disabled: boolean;
  created_at: ISODateTime;
  updated_at: ISODateTime;
}

export interface CreateProjectRequest {
  slug: string;
  name: string;
  environment: Environment;
  storage_profile_id?: string;
  quota_bytes?: number;
  allowed_origins?: string[];
}

export interface UpdateProjectRequest {
  name?: string;
  storage_profile_id?: string;
  quota_bytes?: number | null;
  allowed_origins?: string[];
  disabled?: boolean;
}

export const SLUG_RE = /^[a-z][a-z0-9-]{1,31}$/;

// ---- API keys ---------------------------------------------------------------

export const API_KEY_SCOPES = [
  "uploads:write",
  "assets:read",
  "assets:write",
  "assets:delete",
  "urls:sign",
  "namespaces:admin",
  "imports:write",
] as const;

export type ApiKeyScope = (typeof API_KEY_SCOPES)[number];

export interface ApiKey {
  id: string;
  project_id: string;
  name: string;
  prefix: string;
  scopes: ApiKeyScope[];
  last_used_at: ISODateTime | null;
  expires_at: ISODateTime | null;
  revoked_at: ISODateTime | null;
  created_at: ISODateTime;
}

export interface CreateApiKeyRequest {
  name: string;
  scopes: ApiKeyScope[];
  expires_at?: ISODateTime;
}

export interface CreateApiKeyResponse {
  api_key: ApiKey;
  secret: string;
}

// ---- Storage profiles -------------------------------------------------------

export const STORAGE_PROVIDERS = ["r2", "s3", "b2", "hetzner", "garage"] as const;
export type StorageProvider = (typeof STORAGE_PROVIDERS)[number];

export interface StorageProfileInput {
  name: string;
  provider: StorageProvider;
  endpoint: string;
  region: string;
  public_bucket: string;
  private_bucket: string;
  public_base_url: string;
  private_base_url?: string;
  access_key_id: string;
  secret_access_key: string;
  is_platform: boolean;
}

export type StorageProfilePatch = Partial<StorageProfileInput>;

export interface StorageProfile extends Omit<StorageProfileInput, "secret_access_key"> {
  id: string;
  /** Masked, e.g. `AKIA…3F2Q`. */
  access_key_id: string;
  last_test_at: ISODateTime | null;
  last_test_ok: boolean | null;
  created_at: ISODateTime;
  updated_at: ISODateTime;
}

export type StorageCheckName =
  | "private_put"
  | "private_head"
  | "private_delete"
  | "private_presign_put"
  | "public_put"
  | "public_get_via_base_url"
  | "public_delete"
  | (string & {});

export interface StorageCheck {
  name: StorageCheckName;
  ok: boolean;
  detail: string;
  duration_ms: number;
}

export interface StorageTestResult {
  ok: boolean;
  checks: StorageCheck[];
}

// ---- Presets ----------------------------------------------------------------

export type PresetKind = "image" | "video";

export interface Preset {
  id: string;
  slug: string;
  kind: PresetKind;
  description: string;
  current_version: number;
  params: Record<string, unknown>;
  created_at: ISODateTime;
}

// ---- System -----------------------------------------------------------------

export interface Cap {
  ok: boolean;
  version?: string;
  detail?: string;
}

export const CAPABILITY_KEYS = ["vips", "vips_avif", "ffmpeg", "ffprobe", "rclone", "vaapi"] as const;
export type CapabilityKey = (typeof CAPABILITY_KEYS)[number];

export interface SystemInfo {
  version: string;
  commit: string;
  started_at: ISODateTime;
  uptime_sec: number;
  go_version: string;
  dev: boolean;
  db: { path: string; size_bytes: number; wal_bytes: number };
  tmp: { path: string; free_bytes: number };
  capabilities: Record<CapabilityKey, Cap>;
}

// ---- Audit ------------------------------------------------------------------

export interface AuditEntry {
  id: string;
  actor_type: string;
  actor_id: string | null;
  project_id: string | null;
  action: string;
  target: string | null;
  details: unknown;
  ip: string | null;
  created_at: ISODateTime;
}

export interface AuditPage {
  items: AuditEntry[];
  next_cursor: string | null;
}
