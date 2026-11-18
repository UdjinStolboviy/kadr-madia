-- OAuth 2.1 for MCP clients (docs/AGENTS.md "OAuth"): dynamically registered
-- public clients, authorization grants awaiting a human's consent, and the
-- link from an agent token to the client that obtained it.

CREATE TABLE oauth_clients (
  id            TEXT PRIMARY KEY,                -- oac_
  name          TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,                   -- JSON array
  created_ip    TEXT,
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER
);

CREATE TABLE oauth_grants (
  id              TEXT PRIMARY KEY,              -- oag_
  client_id       TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
  redirect_uri    TEXT NOT NULL,
  code_challenge  TEXT NOT NULL,                 -- S256
  state           TEXT,
  scope           TEXT,
  resource        TEXT,
  status          TEXT NOT NULL CHECK (status IN ('pending','approved','denied','used')),
  code_hash       BLOB,                          -- SHA-256 of the authorization code
  agent_token_id  TEXT REFERENCES agent_tokens(id) ON DELETE CASCADE,
  decided_by      TEXT,
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
);
CREATE INDEX oauth_grants_code ON oauth_grants(code_hash);

ALTER TABLE agent_tokens ADD COLUMN oauth_client_id TEXT REFERENCES oauth_clients(id) ON DELETE SET NULL;
ALTER TABLE agent_tokens ADD COLUMN refresh_hash BLOB;
CREATE UNIQUE INDEX agent_tokens_refresh ON agent_tokens(refresh_hash) WHERE refresh_hash IS NOT NULL;
