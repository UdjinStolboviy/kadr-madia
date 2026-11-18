-- AI agent access (ADR 0014, docs/AGENTS.md): tokens issued by admins to AI
-- agents (MCP clients), the human-in-the-loop approval queue for their
-- risky actions, and per-asset moderation decisions.

CREATE TABLE agent_tokens (
  id               TEXT PRIMARY KEY,               -- agt_
  name             TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  prefix           TEXT NOT NULL UNIQUE,           -- kd_agent_XXXXXXXX
  secret_hash      BLOB NOT NULL,                  -- SHA-256(secret || pepper)
  level            TEXT NOT NULL CHECK (level IN ('read','edit','full')),
  extra_scopes     TEXT NOT NULL DEFAULT '[]',     -- JSON array of sensitive actions
  project_ids      TEXT,                           -- JSON array; NULL = all projects
  approval_mode    TEXT NOT NULL DEFAULT 'none' CHECK (approval_mode IN ('none','destructive','writes')),
  allowed_ips      TEXT NOT NULL DEFAULT '[]',     -- JSON array of CIDRs; empty = any
  max_deletes_per_hour INTEGER NOT NULL DEFAULT 50,
  created_by       TEXT NOT NULL,                  -- admin id (kept after the admin is removed)
  last_used_at     INTEGER,
  last_used_ip     TEXT,
  expires_at       INTEGER,
  revoked_at       INTEGER,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE agent_approvals (
  id             TEXT PRIMARY KEY,                 -- apr_
  token_id       TEXT NOT NULL REFERENCES agent_tokens(id) ON DELETE CASCADE,
  project_id     TEXT,
  action         TEXT NOT NULL,                    -- access action, e.g. assets:delete
  tool           TEXT,                             -- MCP tool name, when called through MCP
  method         TEXT NOT NULL,
  path           TEXT NOT NULL,                    -- /admin/api/... including the query string
  body           BLOB,                             -- AES-256-GCM envelope (may carry credentials)
  content_type   TEXT,
  request_hash   TEXT NOT NULL,                    -- SHA-256 of method, path and body (de-duplication)
  reason         TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('pending','executed','failed','rejected','expired')),
  result_status  INTEGER,
  result_body    BLOB,                             -- AES-256-GCM envelope (may carry secrets)
  decided_by     TEXT,                             -- admin id
  decision_note  TEXT,
  created_at     INTEGER NOT NULL,
  expires_at     INTEGER NOT NULL,
  decided_at     INTEGER
);
CREATE INDEX agent_approvals_status ON agent_approvals(status, created_at DESC);
CREATE INDEX agent_approvals_token ON agent_approvals(token_id, created_at DESC);

CREATE TABLE asset_moderation (
  asset_id     TEXT PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  status       TEXT NOT NULL CHECK (status IN ('approved','flagged','rejected')),
  labels       TEXT NOT NULL DEFAULT '[]',         -- JSON array, e.g. ["nsfw","violence"]
  note         TEXT,
  actor_type   TEXT NOT NULL,                      -- admin | agent
  actor_id     TEXT,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX asset_moderation_project ON asset_moderation(project_id, status, updated_at DESC);
CREATE INDEX audit_log_actor ON audit_log(actor_type, actor_id, id DESC);
