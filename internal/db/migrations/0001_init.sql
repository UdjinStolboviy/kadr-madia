-- Kadr: початкова схема (SQLite, WAL)
-- Рантайм PRAGMA (встановлює застосунок при кожному зʼєднанні):
--   journal_mode=WAL; synchronous=NORMAL; foreign_keys=ON; busy_timeout=5000;
--   temp_store=MEMORY; cache_size=-16000; mmap_size=134217728;
-- Час: INTEGER unix-мілісекунди. ID: TEXT ULID із префіксом типу.

-- ───────────── Адміністратори ─────────────
CREATE TABLE admins (
  id             TEXT PRIMARY KEY,               -- adm_
  email          TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash  TEXT NOT NULL,                  -- argon2id
  totp_secret_enc BLOB,                          -- AES-256-GCM
  role           TEXT NOT NULL DEFAULT 'owner' CHECK (role IN ('owner','admin','viewer')),
  created_at     INTEGER NOT NULL,
  last_login_at  INTEGER
);

CREATE TABLE admin_sessions (
  id_hash     BLOB PRIMARY KEY,                  -- SHA-256 токена сесії
  admin_id    TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  csrf_token  TEXT NOT NULL,
  ip          TEXT,
  user_agent  TEXT,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);
CREATE INDEX admin_sessions_expires ON admin_sessions(expires_at);

-- ───────────── Проєкти та доступ ─────────────
CREATE TABLE projects (
  id               TEXT PRIMARY KEY,             -- prj_
  slug             TEXT NOT NULL UNIQUE,         -- omi, ofi, mif (префікс ключів обʼєктів)
  name             TEXT NOT NULL,
  environment      TEXT NOT NULL DEFAULT 'live' CHECK (environment IN ('live','test')),
  storage_profile_id TEXT REFERENCES storage_profiles(id),
  allowed_origins  TEXT NOT NULL DEFAULT '[]',   -- JSON, для CORS R2 / SDK
  quota_bytes      INTEGER,                      -- NULL = без ліміту
  used_bytes       INTEGER NOT NULL DEFAULT 0,
  reserved_bytes   INTEGER NOT NULL DEFAULT 0,
  disabled_at      INTEGER,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE api_keys (
  id           TEXT PRIMARY KEY,                 -- key_
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  prefix       TEXT NOT NULL UNIQUE,             -- kd_live_ab12cd34 (перші символи, для пошуку й показу)
  secret_hash  BLOB NOT NULL,                    -- SHA-256(secret || pepper)
  scopes       TEXT NOT NULL,                    -- JSON масив
  last_used_at INTEGER,
  expires_at   INTEGER,
  revoked_at   INTEGER,
  created_at   INTEGER NOT NULL
);

CREATE TABLE signing_keys (
  id          TEXT PRIMARY KEY,                  -- kid, напр. omi-k3
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  secret_enc  BLOB NOT NULL,
  status      TEXT NOT NULL CHECK (status IN ('active','verify_only','retired')),
  published_at INTEGER,                          -- коли синхронізовано в kadr-edge
  created_at  INTEGER NOT NULL
);

-- ───────────── Сховища ─────────────
CREATE TABLE storage_profiles (
  id              TEXT PRIMARY KEY,              -- stp_
  name            TEXT NOT NULL,
  provider        TEXT NOT NULL CHECK (provider IN ('r2','s3','b2','hetzner','garage')),
  endpoint        TEXT NOT NULL,
  region          TEXT NOT NULL DEFAULT 'auto',
  public_bucket   TEXT NOT NULL,
  private_bucket  TEXT NOT NULL,
  public_base_url TEXT NOT NULL,                 -- https://cdn.domain
  private_base_url TEXT,                         -- https://media.domain (kadr-edge)
  credentials_enc BLOB NOT NULL,                 -- {access_key_id, secret_access_key}
  is_platform     INTEGER NOT NULL DEFAULT 0,    -- 1 = платформний R2; 0 = BYOS
  last_test_at    INTEGER,
  last_test_ok    INTEGER,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);

-- ───────────── Пресети ─────────────
CREATE TABLE presets (
  id             TEXT PRIMARY KEY,               -- pst_
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slug           TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN ('image','video')),
  description    TEXT,
  current_version INTEGER NOT NULL,
  archived_at    INTEGER,
  created_at     INTEGER NOT NULL,
  UNIQUE (project_id, slug)
);

CREATE TABLE preset_versions (
  preset_id    TEXT NOT NULL REFERENCES presets(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,
  params_json  TEXT NOT NULL,                    -- незмінний після створення
  created_by   TEXT,
  created_at   INTEGER NOT NULL,
  PRIMARY KEY (preset_id, version)
);

-- ───────────── Простори імен (орг / юзер у проєкті) ─────────────
CREATE TABLE namespaces (
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ns             TEXT NOT NULL,                  -- непрозорий ID від проєкту
  quota_bytes    INTEGER,
  quota_assets   INTEGER,
  quota_video_sec_month INTEGER,
  used_bytes     INTEGER NOT NULL DEFAULT 0,
  reserved_bytes INTEGER NOT NULL DEFAULT 0,
  asset_count    INTEGER NOT NULL DEFAULT 0,
  purging_at     INTEGER,
  created_at     INTEGER NOT NULL,
  PRIMARY KEY (project_id, ns)
);

-- ───────────── Завантаження ─────────────
CREATE TABLE uploads (
  id              TEXT PRIMARY KEY,              -- upl_
  project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ns              TEXT NOT NULL,
  preset_id       TEXT NOT NULL REFERENCES presets(id),
  preset_version  INTEGER NOT NULL,
  visibility      TEXT NOT NULL CHECK (visibility IN ('public','private')),
  method          TEXT NOT NULL CHECK (method IN ('PUT','MULTIPART','SERVER','URL','CLOUD')),
  tmp_key         TEXT,                          -- tmp/{project}/{upload_id}
  multipart_id    TEXT,
  filename        TEXT,
  content_type    TEXT,
  declared_bytes  INTEGER,
  declared_sha256 BLOB,
  source_ref      TEXT,                          -- URL або connection_id:file_id
  metadata_json   TEXT NOT NULL DEFAULT '{}',
  status          TEXT NOT NULL CHECK (status IN ('pending','uploaded','processing','done','expired','aborted','failed')),
  asset_id        TEXT,
  expires_at      INTEGER NOT NULL,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX uploads_status_expires ON uploads(status, expires_at);

-- ───────────── Blob і рендиції (фізичні дані) ─────────────
CREATE TABLE blobs (
  id              TEXT PRIMARY KEY,              -- blb_
  project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  storage_profile_id TEXT NOT NULL REFERENCES storage_profiles(id),
  visibility      TEXT NOT NULL CHECK (visibility IN ('public','private')),
  preset_id       TEXT NOT NULL REFERENCES presets(id),
  preset_version  INTEGER NOT NULL,
  source_sha256   BLOB NOT NULL,
  kind            TEXT NOT NULL CHECK (kind IN ('image','video')),
  original_bytes  INTEGER NOT NULL,
  original_width  INTEGER,
  original_height INTEGER,
  original_content_type TEXT,
  original_key    TEXT,                          -- якщо пресет зберігає оригінал
  duration_ms     INTEGER,
  thumbhash       BLOB,
  dominant_color  TEXT,
  total_bytes     INTEGER NOT NULL DEFAULT 0,    -- сума рендицій
  refcount        INTEGER NOT NULL DEFAULT 0,
  status          TEXT NOT NULL CHECK (status IN ('processing','ready','failed','gc_pending','deleted')),
  created_at      INTEGER NOT NULL,
  UNIQUE (project_id, visibility, preset_id, preset_version, source_sha256)
);
CREATE INDEX blobs_gc ON blobs(status) WHERE status = 'gc_pending';

CREATE TABLE renditions (
  blob_id     TEXT NOT NULL REFERENCES blobs(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,                     -- main, poster, hls/720p …
  format      TEXT NOT NULL,                     -- avif, webp, mp4, m3u8
  storage_key TEXT NOT NULL,
  bytes       INTEGER NOT NULL,
  width       INTEGER,
  height      INTEGER,
  bitrate     INTEGER,
  quality     INTEGER,                           -- підібрана q
  over_budget INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (blob_id, name, format)
);

-- ───────────── Асети (логічні файли) ─────────────
CREATE TABLE assets (
  id            TEXT PRIMARY KEY,                -- ast_
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ns            TEXT NOT NULL,
  blob_id       TEXT REFERENCES blobs(id),
  upload_id     TEXT,
  kind          TEXT NOT NULL CHECK (kind IN ('image','video')),
  visibility    TEXT NOT NULL CHECK (visibility IN ('public','private')),
  filename      TEXT,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  status        TEXT NOT NULL CHECK (status IN ('processing','ready','failed','deleted')),
  error_code    TEXT,
  error_detail  TEXT,
  created_at    INTEGER NOT NULL,
  ready_at      INTEGER,
  deleted_at    INTEGER
);
CREATE INDEX assets_ns ON assets(project_id, ns, created_at DESC);
CREATE INDEX assets_blob ON assets(blob_id);

-- ───────────── Черга задач ─────────────
CREATE TABLE jobs (
  id           TEXT PRIMARY KEY,                 -- job_
  kind         TEXT NOT NULL,                    -- process_image | process_video | import_url | import_cloud | copy_visibility | gc_blob | purge_namespace | publish_keys
  pool         TEXT NOT NULL,                    -- image | video | import | maintenance
  priority     INTEGER NOT NULL DEFAULT 100,     -- менше = раніше
  dedupe_key   TEXT UNIQUE,
  payload_json TEXT NOT NULL,
  status       TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','dead','cancelled')),
  attempts     INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  run_at       INTEGER NOT NULL,
  lease_until  INTEGER,
  worker_id    TEXT,
  progress     INTEGER,
  last_error   TEXT,
  created_at   INTEGER NOT NULL,
  finished_at  INTEGER
);
CREATE INDEX jobs_ready ON jobs(pool, status, priority, run_at);
CREATE INDEX jobs_lease ON jobs(status, lease_until) WHERE status = 'running';

-- ───────────── Вебхуки (transactional outbox) ─────────────
CREATE TABLE webhook_endpoints (
  id          TEXT PRIMARY KEY,                  -- whe_
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  url         TEXT NOT NULL,
  secret_enc  BLOB NOT NULL,
  events      TEXT NOT NULL DEFAULT '["*"]',
  disabled_at INTEGER,
  created_at  INTEGER NOT NULL
);

CREATE TABLE events (
  id          TEXT PRIMARY KEY,                  -- evt_ = webhook-id
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);
CREATE INDEX events_project ON events(project_id, created_at DESC);

CREATE TABLE webhook_deliveries (
  id            TEXT PRIMARY KEY,                -- whd_
  event_id      TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  endpoint_id   TEXT NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  status        TEXT NOT NULL CHECK (status IN ('pending','delivering','delivered','dead')),
  attempts      INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  lease_until   INTEGER,
  last_status_code INTEGER,
  last_error    TEXT,
  last_duration_ms INTEGER,
  delivered_at  INTEGER,
  UNIQUE (event_id, endpoint_id)
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries(status, next_attempt_at);

-- ───────────── Хмарні підключення ─────────────
CREATE TABLE cloud_connections (
  id           TEXT PRIMARY KEY,                 -- con_
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ns           TEXT,
  provider     TEXT NOT NULL CHECK (provider IN ('gdrive','onedrive','dropbox')),
  account_label TEXT,
  token_enc    BLOB NOT NULL,                    -- OAuth access/refresh
  token_expires_at INTEGER,
  revoked_at   INTEGER,
  created_at   INTEGER NOT NULL
);

-- ───────────── Ідемпотентність API ─────────────
CREATE TABLE idempotency_keys (
  project_id    TEXT NOT NULL,
  key           TEXT NOT NULL,
  request_hash  BLOB NOT NULL,
  response_code INTEGER NOT NULL,
  response_body TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (project_id, key)
);

-- ───────────── Облік використання ─────────────
CREATE TABLE usage_daily (
  project_id      TEXT NOT NULL,
  ns              TEXT NOT NULL DEFAULT '',
  day             TEXT NOT NULL,                 -- YYYY-MM-DD
  uploads         INTEGER NOT NULL DEFAULT 0,
  assets_ready    INTEGER NOT NULL DEFAULT 0,
  assets_failed   INTEGER NOT NULL DEFAULT 0,
  bytes_in        INTEGER NOT NULL DEFAULT 0,    -- оригінали
  bytes_stored    INTEGER NOT NULL DEFAULT 0,    -- рендиції
  video_sec       INTEGER NOT NULL DEFAULT 0,
  cpu_ms          INTEGER NOT NULL DEFAULT 0,
  dedup_hits      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, ns, day)
);

-- ───────────── Аудит ─────────────
CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_type  TEXT NOT NULL CHECK (actor_type IN ('admin','api_key','system')),
  actor_id    TEXT,
  project_id  TEXT,
  action      TEXT NOT NULL,                     -- project.create, key.revoke, preset.update …
  target      TEXT,
  details_json TEXT,
  ip          TEXT,
  created_at  INTEGER NOT NULL
);
CREATE INDEX audit_log_created ON audit_log(created_at DESC);

-- ───────────── Налаштування ─────────────
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
