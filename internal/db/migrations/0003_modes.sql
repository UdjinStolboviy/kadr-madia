-- Compression modes: the mode used when an upload names neither a preset nor
-- a mode (compact | balanced | quality). The mode presets themselves
-- (image-compact, … video-quality) are ordinary presets seeded per project.
ALTER TABLE projects ADD COLUMN default_mode TEXT NOT NULL DEFAULT 'balanced'
  CHECK (default_mode IN ('compact','balanced','quality'));
