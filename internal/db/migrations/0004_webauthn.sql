-- WebAuthn (passkeys / security keys) for the admin console.
CREATE TABLE webauthn_credentials (
  id            TEXT PRIMARY KEY,                 -- wak_
  admin_id      TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  credential_id BLOB NOT NULL UNIQUE,             -- authenticator-assigned id
  public_key    BLOB NOT NULL,                    -- COSE key
  sign_count    INTEGER NOT NULL DEFAULT 0,
  transports    TEXT NOT NULL DEFAULT '',         -- comma separated
  aaguid        BLOB,
  flags         INTEGER NOT NULL DEFAULT 0,       -- raw authenticator flags byte (UP/UV/BE/BS)
  name          TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER
);
CREATE INDEX webauthn_credentials_admin ON webauthn_credentials(admin_id);
