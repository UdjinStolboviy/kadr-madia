-- Scheduled agent runs (docs/AGENTS.md "Scheduled runs"): Kadr runs Claude
-- with the MCP tools of an agent token on a schedule (e.g. nightly
-- moderation, a morning health report) and keeps the run history.

CREATE TABLE agent_schedules (
  id           TEXT PRIMARY KEY,                 -- ags_
  name         TEXT NOT NULL,
  token_id     TEXT NOT NULL REFERENCES agent_tokens(id) ON DELETE CASCADE,
  prompt       TEXT NOT NULL,
  frequency    TEXT NOT NULL CHECK (frequency IN ('hourly','daily','weekly')),
  time_utc     TEXT NOT NULL DEFAULT '03:00',    -- HH:MM (hourly: only the minute counts)
  weekday      INTEGER NOT NULL DEFAULT 1,       -- 0 = Sunday (weekly only)
  max_turns    INTEGER NOT NULL DEFAULT 30,
  notify       INTEGER NOT NULL DEFAULT 1,       -- send the report to Telegram
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_by   TEXT NOT NULL,
  last_run_at  INTEGER,
  next_run_at  INTEGER,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE agent_runs (
  id            TEXT PRIMARY KEY,                -- agr_
  schedule_id   TEXT REFERENCES agent_schedules(id) ON DELETE SET NULL,
  token_id      TEXT NOT NULL REFERENCES agent_tokens(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL CHECK (trigger IN ('schedule','manual')),
  prompt        TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  output        TEXT,
  error         TEXT,
  tool_calls    INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  model         TEXT,
  created_by    TEXT,
  created_at    INTEGER NOT NULL,
  started_at    INTEGER,
  finished_at   INTEGER
);
CREATE INDEX agent_runs_schedule ON agent_runs(schedule_id, created_at DESC);
CREATE INDEX agent_runs_created ON agent_runs(created_at DESC);
