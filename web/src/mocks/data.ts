// Seed data for mock mode. Only imported by src/mocks/server.ts (dev, VITE_MOCK=1).
import type { Admin, ApiKey, AuditEntry, Preset, Project, StorageProfile } from "@/lib/types";

export interface MockDb {
  admin: Admin & { password: string; totp_secret: string | null; pending_totp_secret: string | null };
  projects: Project[];
  apiKeys: ApiKey[];
  storage: (StorageProfile & { secret: string; access_key_full: string })[];
  presets: Record<string, Preset[]>;
  audit: AuditEntry[];
  seq: number;
}

const now = Date.now();
const ago = (ms: number) => new Date(now - ms).toISOString();
const H = 3600_000;
const D = 24 * H;

export function ulid(prefix = ""): string {
  const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let s = "";
  for (let i = 0; i < 20; i++) s += alphabet[Math.floor(Math.random() * 32)];
  return prefix + (Date.now().toString(32).toUpperCase() + s).slice(0, 26 - prefix.length);
}

const fileLimits = { maxInputBytes: 104857600, accept: ["pdf", "docx", "xlsx", "pptx", "odt", "ods", "odp", "rtf", "txt", "csv"] };

export function defaultPresets(projectId: string, createdAt: string): Preset[] {
  const mk = (slug: string, kind: "image" | "video" | "file", description: string, params: Record<string, unknown>): Preset => ({
    id: ulid("pst_"),
    slug,
    kind,
    description,
    current_version: 1,
    params,
    created_at: createdAt,
  });
  const limits = {
    maxInputBytes: 26214400,
    maxInputPixels: 40000000,
    accept: ["jpeg", "png", "webp", "heic", "avif", "gif"],
  };
  const outputs = [
    { name: "main", format: "avif", effort: 4, chroma: "4:2:0" },
    { name: "main", format: "webp", smartSubsample: true },
  ];
  void projectId;
  return [
    mk("photo-lite", "image", "Cards, inventory, thumbnails", {
      kind: "image",
      resize: { maxWidth: 800, maxHeight: 800, fit: "inside", crop: null, aspect: null },
      color: { grayscale: false, normalise: false },
      sharpen: { sigma: 0.6, m1: 0.5, m2: 1.5 },
      outputs,
      quality: { mode: "budget", maxBytes: 20480, qMax: 60, qMin: 30, downscaleStep: 0.85, minSide: 320 },
      placeholder: { thumbhash: true, dominantColor: true },
      limits,
      retainOriginal: false,
    }),
    mk("photo-balanced", "image", "Full-screen viewing", {
      kind: "image",
      resize: { maxWidth: 1440, maxHeight: 1440, fit: "inside", crop: null, aspect: null },
      color: { grayscale: false, normalise: false },
      sharpen: null,
      outputs: [
        { name: "main", format: "avif", effort: 4, chroma: "4:2:0" },
        { name: "main", format: "webp", smartSubsample: true },
      ],
      quality: { mode: "ssim", target: 0.985 },
      placeholder: { thumbhash: true, dominantColor: true },
      limits,
      retainOriginal: false,
    }),
    mk("doc-lite", "image", "Receipts, contracts, labels", {
      kind: "image",
      resize: { maxWidth: 1100, maxHeight: 1100, fit: "inside", crop: null, aspect: null },
      color: { grayscale: true, normalise: true },
      sharpen: { sigma: 0.85, m1: 0.5, m2: 1.5 },
      outputs,
      quality: { mode: "budget", maxBytes: 20480, qMax: 60, qMin: 30, downscaleStep: 0.85, minSide: 480 },
      placeholder: { thumbhash: false, dominantColor: false },
      limits,
      retainOriginal: false,
    }),
    mk("avatar", "image", "User avatars", {
      kind: "image",
      resize: { maxWidth: 256, maxHeight: 256, fit: "cover", crop: "attention", aspect: "1:1" },
      color: { grayscale: false, normalise: false },
      sharpen: { sigma: 0.5, m1: 0.5, m2: 1.5 },
      outputs,
      quality: { mode: "budget", maxBytes: 8192, qMax: 60, qMin: 30, downscaleStep: 0.85, minSide: 128 },
      placeholder: { thumbhash: true, dominantColor: true },
      limits,
      retainOriginal: false,
    }),
    mk("video-720p", "video", "Regular clips", {
      kind: "video",
      video: { codec: "h264", maxWidth: 1280, fps: 30, crf: 26, maxrate: "900k", bufsize: "1800k", preset: "slow", hw: "auto" },
      audio: { codec: "aac", bitrate: "64k", channels: 1, sampleRate: 44100 },
      container: { format: "mp4", faststart: true },
      poster: { atSec: 1.0, preset: "photo-lite", maxBytes: 15360 },
      limits: { maxInputBytes: 2147483648, maxDurationSec: 300, maxWidth: 4096, maxHeight: 4096 },
      retainOriginal: false,
    }),
    mk("video-480p-eco", "video", "Economy", {
      kind: "video",
      video: { codec: "h264", maxWidth: 854, fps: 30, crf: 28, maxrate: "500k", bufsize: "1000k", preset: "slow", hw: "auto" },
      audio: { codec: "aac", bitrate: "48k", channels: 1, sampleRate: 44100 },
      container: { format: "mp4", faststart: true },
      poster: { atSec: 1.0, preset: "photo-lite", maxBytes: 15360 },
      limits: { maxInputBytes: 2147483648, maxDurationSec: 300, maxWidth: 4096, maxHeight: 4096 },
      retainOriginal: false,
    }),
    mk("file-original", "file", "Documents stored exactly as uploaded (validated by content); PDF thumbnail ≤ 20 KB", {
      kind: "file",
      limits: fileLimits,
      optimize: { strategy: "none", linearize: false, minSavingsPercent: 0 },
      thumbnail: { enabled: true, maxSide: 640, maxBytes: 20480 },
      retainOriginal: false,
    }),
    ...modePresets(projectId, createdAt),
  ];
}

/** The six compression-mode presets (mirrors internal/preset/modes.go). */
export function modePresets(projectId: string, createdAt: string): Preset[] {
  void projectId;
  const limits = { maxInputBytes: 26214400, maxInputPixels: 40000000, accept: ["jpeg", "png", "webp", "heic", "avif", "gif"] };
  const std = [
    { name: "main", format: "avif", effort: 4, chroma: "4:2:0" },
    { name: "main", format: "webp", smartSubsample: true },
  ];
  const hq = [
    { name: "main", format: "avif", effort: 4, chroma: "4:4:4" },
    { name: "main", format: "webp" },
  ];
  const img = (mode: string, description: string, side: number, quality: Record<string, unknown>, outputs: unknown[], sharpen: unknown): Preset => ({
    id: ulid("pst_"),
    slug: `image-${mode}`,
    kind: "image",
    description,
    current_version: 1,
    created_at: createdAt,
    params: {
      kind: "image",
      resize: { maxWidth: side, maxHeight: side, fit: "inside", crop: null, aspect: null },
      color: { grayscale: false, normalise: false },
      sharpen,
      outputs,
      quality,
      placeholder: { thumbhash: true, dominantColor: true },
      limits,
      retainOriginal: false,
    },
  });
  const vid = (mode: string, description: string, video: Record<string, unknown>, audio: Record<string, unknown>): Preset => ({
    id: ulid("pst_"),
    slug: `video-${mode}`,
    kind: "video",
    description,
    current_version: 1,
    created_at: createdAt,
    params: {
      kind: "video",
      video: { codec: "h264", fps: 30, preset: "slow", hw: "auto", ...video },
      audio: { codec: "aac", ...audio },
      container: { format: "mp4", faststart: true },
      poster: { atSec: 1.0, preset: "photo-lite", maxBytes: 15360 },
      limits: { maxInputBytes: 2147483648, maxDurationSec: 300, maxWidth: 4096, maxHeight: 4096 },
      retainOriginal: false,
    },
  });
  const doc = (mode: string, description: string, optimize: Record<string, unknown>, thumbnail: Record<string, unknown>): Preset => ({
    id: ulid("pst_"),
    slug: `file-${mode}`,
    kind: "file",
    description,
    current_version: 1,
    created_at: createdAt,
    params: { kind: "file", limits: fileLimits, optimize, thumbnail, retainOriginal: false },
  });
  return [
    img("compact", "Compact: good quality at a very small size (max side 1080, ≤ 30 KB)", 1080, { mode: "budget", maxBytes: 30720, qMax: 55, qMin: 25, downscaleStep: 0.85, minSide: 480 }, std, { sigma: 0.6, m1: 0.5, m2: 1.5 }),
    img("balanced", "Balanced: the golden middle (max side 1440, SSIM ≥ 0.975)", 1440, { mode: "ssim", target: 0.975, qMin: 35, qMax: 65 }, std, null),
    img("quality", "Quality: ideal quality at the smallest size that achieves it (max side 2048, SSIM ≥ 0.99, 4:4:4)", 2048, { mode: "ssim", target: 0.99, qMin: 45, qMax: 85 }, hq, null),
    vid("compact", "Compact: 480p H.264 CRF 30, ≤ 450 kbit/s, AAC 48k mono", { maxWidth: 854, crf: 30, maxrate: "450k", bufsize: "900k" }, { bitrate: "48k", channels: 1, sampleRate: 44100 }),
    vid("balanced", "Balanced: 720p H.264 CRF 26, ≤ 1.2 Mbit/s, AAC 96k stereo", { maxWidth: 1280, crf: 26, maxrate: "1200k", bufsize: "2400k" }, { bitrate: "96k", channels: 2, sampleRate: 44100 }),
    vid("quality", "Quality: 1080p H.264 CRF 20, ≤ 5 Mbit/s, AAC 160k stereo", { maxWidth: 1920, crf: 20, maxrate: "5000k", bufsize: "10000k" }, { bitrate: "160k", channels: 2, sampleRate: 48000 }),
    doc("compact", "Compact: PDF images re-encoded at 110 dpi / JPEG 60, linearized; thumbnail ≤ 12 KB", { strategy: "downsample", imageDpi: 110, jpegQuality: 60, linearize: true, minSavingsPercent: 5 }, { enabled: true, maxSide: 480, maxBytes: 12288 }),
    doc("balanced", "Balanced: PDF images at 150 dpi / JPEG 75, linearized; thumbnail ≤ 20 KB", { strategy: "downsample", imageDpi: 150, jpegQuality: 75, linearize: true, minSavingsPercent: 5 }, { enabled: true, maxSide: 640, maxBytes: 20480 }),
    doc("quality", "Quality: lossless PDF recompression (no visible change), linearized; thumbnail ≤ 30 KB", { strategy: "lossless", linearize: true, minSavingsPercent: 3 }, { enabled: true, maxSide: 800, maxBytes: 30720 }),
  ];
}

export function seed(empty: boolean): MockDb {
  const admin: MockDb["admin"] = {
    id: "adm_01JC9Z3T7Q8K4M2N6P0R5S1V3W",
    email: "admin@example.com",
    role: "owner",
    totp_enabled: false,
    created_at: ago(30 * D),
    last_login_at: ago(2 * D),
    password: "kadr-admin",
    totp_secret: null,
    pending_totp_secret: null,
  };
  if (empty) {
    return { admin, projects: [], apiKeys: [], storage: [], presets: {}, audit: [], seq: 0 };
  }

  const r2Id = "stp_01JC9Z4A1B2C3D4E5F6G7H8J9K";
  const b2Id = "stp_01JCA05M6N7P8Q9R0S1T2V3W4X";
  const storage: MockDb["storage"] = [
    {
      id: r2Id,
      name: "R2 production",
      provider: "r2",
      endpoint: "https://8f3c2a1b9d7e4f60a5b4c3d2e1f00a9b.r2.cloudflarestorage.com",
      region: "auto",
      public_bucket: "kadr-public",
      private_bucket: "kadr-private",
      public_base_url: "https://cdn.example.com",
      private_base_url: "https://media.example.com",
      access_key_id: "1f2e…9a0b",
      access_key_full: "1f2e3d4c5b6a79881f2e3d4c5b6a9a0b",
      secret: "mock-secret",
      is_platform: true,
      last_test_at: ago(3 * H),
      last_test_ok: true,
      created_at: ago(29 * D),
      updated_at: ago(3 * H),
    },
    {
      id: b2Id,
      name: "B2 archive",
      provider: "b2",
      endpoint: "https://s3.eu-central-003.backblazeb2.com",
      region: "eu-central-003",
      public_bucket: "acme-archive-public",
      private_bucket: "acme-archive-private",
      public_base_url: "https://archive.example.com",
      access_key_id: "0035…77QZ",
      access_key_full: "003512ab34cd56ef7890ab0000000077QZ",
      secret: "mock-secret",
      is_platform: false,
      last_test_at: ago(2 * D),
      last_test_ok: false,
      created_at: ago(12 * D),
      updated_at: ago(2 * D),
    },
  ];

  const p1 = "prj_01JC9Z5B2C3D4E5F6G7H8J9K0M";
  const p2 = "prj_01JC9Z6C3D4E5F6G7H8J9K0M1N";
  const p3 = "prj_01JCB17D4E5F6G7H8J9K0M1N2P";
  const projects: Project[] = [
    {
      id: p1,
      slug: "acme",
      name: "Acme",
      environment: "live",
      storage_profile_id: r2Id,
      allowed_origins: ["https://app.example.com", "https://www.example.com"],
      quota_bytes: 50 * 1024 ** 3,
      used_bytes: 18.4 * 1024 ** 3,
      disabled: false,
      created_at: ago(28 * D),
      updated_at: ago(5 * D),
    },
    {
      id: p2,
      slug: "acme-staging",
      name: "Acme staging",
      environment: "test",
      storage_profile_id: r2Id,
      allowed_origins: ["https://staging.example.com", "http://localhost:3000"],
      quota_bytes: 5 * 1024 ** 3,
      used_bytes: 4.1 * 1024 ** 3,
      disabled: false,
      created_at: ago(27 * D),
      updated_at: ago(1 * D),
    },
    {
      id: p3,
      slug: "demo",
      name: "Demo",
      environment: "test",
      storage_profile_id: null,
      allowed_origins: [],
      quota_bytes: null,
      used_bytes: 312 * 1024 ** 2,
      disabled: true,
      created_at: ago(9 * D),
      updated_at: ago(4 * D),
    },
  ];

  const apiKeys: ApiKey[] = [
    {
      id: "key_01JC9Z7D4E5F6G7H8J9K0M1N2P",
      project_id: p1,
      name: "backend-production",
      prefix: "kd_live_7Hq2LmX9",
      scopes: ["uploads:write", "assets:read", "assets:write", "urls:sign"],
      last_used_at: ago(4 * 60_000),
      expires_at: null,
      revoked_at: null,
      created_at: ago(27 * D),
    },
    {
      id: "key_01JC9Z8E5F6G7H8J9K0M1N2P3Q",
      project_id: p1,
      name: "import-worker",
      prefix: "kd_live_Pz81QaW3",
      scopes: ["imports:write", "assets:read"],
      last_used_at: ago(6 * D),
      expires_at: new Date(now + 4 * D).toISOString(),
      revoked_at: null,
      created_at: ago(20 * D),
    },
    {
      id: "key_01JC9Z9F6G7H8J9K0M1N2P3Q4R",
      project_id: p1,
      name: "old-backend",
      prefix: "kd_live_a0B1c2D3",
      scopes: ["uploads:write", "assets:read"],
      last_used_at: ago(22 * D),
      expires_at: null,
      revoked_at: ago(21 * D),
      created_at: ago(28 * D),
    },
    {
      id: "key_01JCA0AG7H8J9K0M1N2P3Q4R5S",
      project_id: p2,
      name: "staging",
      prefix: "kd_test_Xy7Zk2Pq",
      scopes: ["uploads:write", "assets:read", "assets:delete", "urls:sign", "namespaces:admin"],
      last_used_at: ago(2 * H),
      expires_at: null,
      revoked_at: null,
      created_at: ago(26 * D),
    },
  ];

  const presets: Record<string, Preset[]> = {
    [p1]: defaultPresets(p1, projects[0]!.created_at),
    [p2]: defaultPresets(p2, projects[1]!.created_at),
    [p3]: defaultPresets(p3, projects[2]!.created_at),
  };
  // Show a bumped version on one preset.
  presets[p1]![0]!.current_version = 3;

  const actions: [string, string | null, string | null, unknown][] = [
    ["auth.login", null, "admin@example.com", { method: "password" }],
    ["api_key.create", p1, "key_01JC9Z7D4E5F6G7H8J9K0M1N2P", { name: "backend-production" }],
    ["project.update", p2, p2, { changed: ["allowed_origins"] }],
    ["storage_profile.test", null, r2Id, { ok: true, duration_ms: 812 }],
    ["api_key.revoke", p1, "key_01JC9Z9F6G7H8J9K0M1N2P3Q4R", null],
    ["project.disable", p3, p3, {}],
    ["storage_profile.create", null, b2Id, { provider: "b2" }],
    ["auth.login_failed", null, "admin@example.com", { reason: "invalid_credentials" }],
  ];
  const audit: AuditEntry[] = [];
  for (let i = 0; i < 120; i++) {
    const [action, project_id, target, details] = actions[i % actions.length]!;
    audit.push({
      id: `aud_${String(1000 - i).padStart(6, "0")}`,
      actor_type: action.startsWith("auth.") ? "admin" : i % 11 === 0 ? "system" : "admin",
      actor_id: admin.id,
      project_id,
      action,
      target,
      details,
      ip: i % 3 === 0 ? "203.0.113.24" : "198.51.100.7",
      created_at: ago(i * 3.7 * H + 5 * 60_000),
    });
  }

  return { admin, projects, apiKeys, storage, presets, audit, seq: 0 };
}
