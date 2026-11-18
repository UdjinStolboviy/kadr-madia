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
  default_mode?: CompressionMode;
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
  default_mode?: CompressionMode;
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

export type PresetKind = "image" | "video" | "file";

export interface Preset {
  id: string;
  slug: string;
  kind: PresetKind;
  description: string;
  current_version: number;
  params: Record<string, unknown>;
  created_at: ISODateTime;
}

// ---- Compression modes ------------------------------------------------------

export const COMPRESSION_MODES = ["compact", "balanced", "quality"] as const;
export type CompressionMode = (typeof COMPRESSION_MODES)[number];

export interface ModeInfo {
  mode: CompressionMode;
  description: string;
  image: Preset;
  video: Preset;
  file?: Preset;
  customized: { image: boolean; video: boolean; file?: boolean };
}

export interface ModesResponse {
  default_mode: CompressionMode;
  modes: ModeInfo[];
}

/** Video preset params (docs/PIPELINES.md section 2). */
export interface VideoParams {
  kind?: string;
  video: { codec?: string; maxWidth: number; fps: number; crf: number; maxrate?: string; bufsize?: string; preset?: string; hw?: string };
  audio: { codec?: string; bitrate?: string; channels?: number; sampleRate?: number };
  container?: { format?: string; faststart?: boolean };
  poster?: { atSec?: number; preset?: string; maxBytes?: number };
  limits?: Record<string, unknown>;
  retainOriginal?: boolean;
  /** Also produce an adaptive HLS ladder. */
  hls?: boolean;
  [k: string]: unknown;
}

export const FILE_FORMATS = ["pdf", "docx", "xlsx", "pptx", "odt", "ods", "odp", "rtf", "txt", "csv"] as const;
export type FileFormat = (typeof FILE_FORMATS)[number];
export const FILE_STRATEGIES = ["none", "lossless", "downsample"] as const;
export type FileStrategy = (typeof FILE_STRATEGIES)[number];

/** Document preset params (docs/PIPELINES.md section 2b). */
export interface FileParams {
  kind?: string;
  limits: { maxInputBytes: number; accept: string[] };
  optimize: { strategy: FileStrategy; imageDpi?: number; jpegQuality?: number; linearize: boolean; minSavingsPercent: number };
  thumbnail: { enabled: boolean; maxSide: number; maxBytes: number };
  retainOriginal: boolean;
  [k: string]: unknown;
}

// ---- System -----------------------------------------------------------------

export interface Cap {
  ok: boolean;
  version?: string;
  detail?: string;
}

export const CAPABILITY_KEYS = ["vips", "vips_avif", "ffmpeg", "ffprobe", "rclone", "vaapi", "ghostscript", "qpdf"] as const;
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

// ---- Jobs -------------------------------------------------------------------

export const JOB_STATUSES = ["queued", "running", "succeeded", "dead", "cancelled"] as const;
export const JOB_POOLS = ["image", "video", "maintenance", "webhook"] as const;

export interface Job {
  id: string;
  kind: string;
  pool: string;
  status: string;
  attempts: number;
  max_attempts: number;
  /** 0-100 for running video jobs, otherwise null. */
  progress: number | null;
  last_error: string | null;
  run_at: ISODateTime;
  created_at: ISODateTime;
  finished_at: ISODateTime | null;
}

export interface JobFilters {
  status?: string;
  pool?: string;
  limit?: number;
}

// ---- Assets -----------------------------------------------------------------

export const ASSET_KINDS = ["image", "video", "file"] as const;
export const ASSET_STATUSES = ["processing", "ready", "failed"] as const;

export interface AssetOriginal {
  filename: string | null;
  content_type: string | null;
  bytes: number;
  width: number | null;
  height: number | null;
}

export interface Rendition {
  name: string;
  format: string;
  bytes: number;
  width: number | null;
  height: number | null;
  /** Present for public assets. */
  url?: string;
  /** Present for private assets. */
  key?: string;
}

export interface Asset {
  id: string;
  status: string;
  kind: string;
  namespace: string;
  visibility: string;
  preset: string;
  preset_version: number;
  original: AssetOriginal;
  renditions: Rendition[];
  placeholder: { thumbhash: string | null; dominant_color: string | null } | null;
  duration_sec: number | null;
  poster: Rendition | null;
  metadata: Record<string, unknown>;
  savings_ratio: number | null;
  error?: { code: string; detail?: string };
  created_at: ISODateTime;
  ready_at: ISODateTime | null;
}

export interface AssetPage {
  items: Asset[];
  next_cursor: string | null;
}

export interface AssetFilters {
  namespace?: string;
  kind?: string;
  status?: string;
}

// ---- Analytics --------------------------------------------------------------

export const ANALYTICS_DAYS = [7, 30, 90] as const;

export interface UsageDay {
  day: string;
  uploads: number;
  assets_ready: number;
  assets_failed: number;
  bytes_in: number;
  bytes_stored: number;
  video_sec: number;
  cpu_ms: number;
  dedup_hits: number;
}

export interface Analytics {
  from: string;
  to: string;
  days: UsageDay[];
  totals: UsageDay;
  savings_ratio: number;
  failed_rate: number;
}

// ---- Playground -------------------------------------------------------------

/** Image preset params (docs/PIPELINES.md §1); only the parts the playground edits are typed strictly. */
export interface ImageParams {
  kind?: string;
  resize: { maxWidth: number; maxHeight: number; fit: string; crop?: string | null; aspect?: string | null };
  color: { grayscale: boolean; normalise: boolean };
  sharpen?: { sigma: number; m1: number; m2: number } | null;
  outputs: { name: string; format: string; effort?: number; chroma?: string; smartSubsample?: boolean }[];
  quality: {
    mode: "budget" | "ssim" | "fixed";
    maxBytes?: number;
    qMax?: number;
    qMin?: number;
    downscaleStep?: number;
    minSide?: number;
    q?: number;
    target?: number;
  };
  placeholder?: { thumbhash: boolean; dominantColor: boolean };
  limits?: Record<string, unknown>;
  retainOriginal?: boolean;
  [k: string]: unknown;
}

export interface PlaygroundRendition {
  name: string;
  format: string;
  content_type: string;
  bytes: number;
  width: number;
  height: number;
  quality: number;
  iterations: number;
  over_budget: boolean;
  ssim: number;
  /** Base64 of the encoded file. */
  data: string;
}

export interface PlaygroundResult {
  source: { format: string; bytes: number; width: number; height: number };
  renditions: PlaygroundRendition[];
  elapsed_ms: number;
  dominant_color?: string;
  thumbhash?: string;
}

export interface SavePresetVersionRequest {
  params: ImageParams | VideoParams | FileParams;
  description?: string;
}

// ---- Webhooks ---------------------------------------------------------------

export const WEBHOOK_EVENTS = [
  "*",
  "asset.*",
  "asset.ready",
  "asset.failed",
  "asset.updated",
  "asset.deleted",
  "upload.*",
  "upload.expired",
  "quota.*",
  "quota.warning",
  "quota.exceeded",
  "namespace.*",
  "namespace.purged",
] as const;

export interface WebhookEndpoint {
  id: string;
  project_id: string;
  url: string;
  events: string[];
  disabled: boolean;
  created_at: ISODateTime;
}

export interface CreateWebhookRequest {
  url: string;
  events?: string[];
}

export interface CreateWebhookResponse {
  webhook: WebhookEndpoint;
  secret: string;
}

export const DELIVERY_STATUSES = ["pending", "delivering", "delivered", "dead"] as const;

export interface Delivery {
  id: string;
  event_id: string;
  event_type: string;
  endpoint_id: string;
  endpoint_url: string;
  status: string;
  attempts: number;
  next_attempt_at: ISODateTime | null;
  last_status_code: number | null;
  last_error: string | null;
  last_duration_ms: number | null;
  delivered_at: ISODateTime | null;
  created_at: ISODateTime;
}

export interface DeliveryPage {
  items: Delivery[];
  next_cursor: string | null;
}

// ---- Signing keys -----------------------------------------------------------

export interface SigningKey {
  id: string;
  project_id: string;
  /** active | verify_only | retired */
  status: string;
  published_at: ISODateTime | null;
  created_at: ISODateTime;
}

export interface CreateSigningKeyResponse {
  key: SigningKey;
  secret: string;
}

// ---- Cloud connections ------------------------------------------------------

export const CLOUD_PROVIDERS = ["gdrive", "onedrive", "dropbox"] as const;
export type CloudProvider = (typeof CLOUD_PROVIDERS)[number];

export interface CloudConnection {
  id: string;
  project_id: string;
  namespace: string | null;
  provider: string;
  account_label: string | null;
  created_at: ISODateTime;
}

export interface CreateCloudConnectionRequest {
  provider: CloudProvider;
  account_label?: string;
  namespace?: string;
  /** rclone OAuth token JSON. */
  token: string;
}

// ---- WebAuthn passkeys ------------------------------------------------------

export interface Passkey {
  id: string;
  name: string;
  transports: string[];
  created_at: ISODateTime;
  last_used_at: ISODateTime | null;
}

export interface WebAuthnCeremony {
  ceremony_id: string;
  /** JSON-serialized PublicKeyCredential{Creation,Request}Options ({ publicKey: … }). */
  options: { publicKey: Record<string, unknown> };
}

// ---- AI agents (docs/AGENTS.md) ---------------------------------------------

export const AGENT_LEVELS = ["read", "edit", "full"] as const;
export type AgentLevel = (typeof AGENT_LEVELS)[number];

export const AGENT_APPROVAL_MODES = ["none", "destructive", "writes"] as const;
export type AgentApprovalMode = (typeof AGENT_APPROVAL_MODES)[number];

export const AGENT_SENSITIVE_SCOPES = ["keys:issue", "storage:manage", "edge:publish", "billing:manage", "cloud:connect"] as const;
export type AgentSensitiveScope = (typeof AGENT_SENSITIVE_SCOPES)[number];

export type AgentStatus = "active" | "expired" | "revoked";

export interface Agent {
  id: string;
  name: string;
  description: string;
  prefix: string;
  level: AgentLevel;
  extra_scopes: string[];
  permissions: string[];
  /** null = all projects. */
  project_ids: string[] | null;
  approval_mode: AgentApprovalMode;
  allowed_ips: string[];
  max_deletes_per_hour: number;
  status: AgentStatus;
  created_by: string;
  /** Set when the token was obtained through OAuth (e.g. a claude.ai connector). */
  oauth_client_id: string | null;
  last_used_at: ISODateTime | null;
  last_used_ip: string | null;
  expires_at: ISODateTime | null;
  revoked_at: ISODateTime | null;
  created_at: ISODateTime;
  updated_at: ISODateTime;
}

export interface AgentInput {
  name?: string;
  description?: string;
  level?: AgentLevel;
  extra_scopes?: string[];
  project_ids?: string[] | null;
  approval_mode?: AgentApprovalMode;
  allowed_ips?: string[];
  max_deletes_per_hour?: number;
  /** 0 = never. */
  expires_in_days?: number;
}

export interface McpSetup {
  url: string;
  claude_code_command: string;
  json_config: string;
}

export interface AgentSecretResponse {
  agent: Agent;
  token: string;
  mcp: McpSetup;
}

export interface AgentSettings {
  enabled: boolean;
  pending_approvals: number;
  mcp_url: string;
  levels: Record<AgentLevel, string[]>;
  sensitive_scopes: string[];
}

export const APPROVAL_STATUSES = ["pending", "executed", "failed", "rejected", "expired"] as const;
export type ApprovalStatus = (typeof APPROVAL_STATUSES)[number];

export interface AgentApproval {
  id: string;
  agent_id: string;
  agent_name?: string;
  project_id: string | null;
  action: string;
  tool: string | null;
  method: string;
  path: string;
  body: unknown;
  reason: string;
  status: ApprovalStatus;
  result_status: number | null;
  result: unknown;
  decided_by: string | null;
  decision_note: string | null;
  created_at: ISODateTime;
  expires_at: ISODateTime;
  decided_at: ISODateTime | null;
}

export interface AuditFilters {
  actor_type?: string;
  actor_id?: string;
  project_id?: string;
}

// ---- Moderation ---------------------------------------------------------------

export const MODERATION_STATUSES = ["unreviewed", "approved", "flagged", "rejected"] as const;
export type ModerationStatus = (typeof MODERATION_STATUSES)[number];
export type ModerationDecision = Exclude<ModerationStatus, "unreviewed">;

export interface Moderation {
  asset_id: string;
  status: ModerationStatus;
  labels: string[];
  note: string | null;
  actor_type: string | null;
  actor_id: string | null;
  updated_at: ISODateTime | null;
}

export interface ModerationItem {
  asset_id: string;
  kind: string;
  namespace: string;
  visibility: string;
  created_at: ISODateTime;
  moderation: Moderation;
}

export interface ModerationPage {
  items: ModerationItem[];
  next_cursor: string | null;
}

export interface SetModerationRequest {
  status: ModerationDecision;
  labels?: string[];
  note?: string;
}

// ---- OAuth consent --------------------------------------------------------------

export interface OAuthGrant {
  id: string;
  client_id: string;
  client_name: string;
  redirect_host: string;
  redirect_uri: string;
  scope: string | null;
  suggested_level: AgentLevel;
  client_registered_at: ISODateTime;
  expires_at: ISODateTime;
}

export interface OAuthApproveResponse {
  redirect_url: string;
  agent: Agent;
}

// ---- AI operations ----------------------------------------------------------------

export interface AiStatus {
  available: boolean;
  classifier: string | null;
  agent_runs: boolean;
  model: string | null;
}

export const AI_MODERATION_MODES = ["flag_only", "decide"] as const;
export type AiModerationMode = (typeof AI_MODERATION_MODES)[number];

export interface ModerationSettings {
  enabled: boolean;
  policy: string;
  mode: AiModerationMode;
  kinds: string[];
  since?: string;
}

export interface ModerationSettingsResponse {
  settings: ModerationSettings;
  ai: AiStatus;
}

export const SCHEDULE_FREQUENCIES = ["hourly", "daily", "weekly"] as const;
export type ScheduleFrequency = (typeof SCHEDULE_FREQUENCIES)[number];

export interface AgentSchedule {
  id: string;
  name: string;
  agent_id: string;
  agent_name: string;
  prompt: string;
  frequency: ScheduleFrequency;
  /** HH:MM in UTC (hourly: only the minute counts). */
  time_utc: string;
  /** 0 = Sunday (weekly only). */
  weekday: number;
  max_turns: number;
  notify: boolean;
  enabled: boolean;
  last_run_at: ISODateTime | null;
  next_run_at: ISODateTime | null;
  created_at: ISODateTime;
}

export interface ScheduleInput {
  name?: string;
  agent_id?: string;
  prompt?: string;
  frequency?: ScheduleFrequency;
  time_utc?: string;
  weekday?: number;
  max_turns?: number;
  notify?: boolean;
  enabled?: boolean;
}

export interface SchedulesResponse {
  items: AgentSchedule[];
  ai: AiStatus;
}

export type AgentRunStatus = "queued" | "running" | "succeeded" | "failed";

export interface AgentRun {
  id: string;
  schedule_id: string | null;
  agent_id: string;
  agent_name: string;
  trigger: "schedule" | "manual";
  prompt: string;
  status: AgentRunStatus;
  output: string | null;
  error: string | null;
  tool_calls: number;
  input_tokens: number;
  output_tokens: number;
  model: string | null;
  created_at: ISODateTime;
  started_at: ISODateTime | null;
  finished_at: ISODateTime | null;
}
