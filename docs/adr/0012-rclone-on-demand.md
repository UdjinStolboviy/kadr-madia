# ADR-0012: rclone as a one-off process for cloud imports

**Status:** Accepted

## Context
We need imports from Google Drive, OneDrive and Dropbox. `rclone rcd` as a resident daemon consumes memory and keeps credentials in its own config; rclone as a Go library inflates the binary and dependency tree.

## Decision
The OAuth flow and token storage (encrypted) live in Kadr. Per job, a temporary `rclone.conf` (0600, in the job's working directory) is generated, `rclone copyto remote:path tmpfile` runs, and the config is deleted; a refreshed token is read back and stored.

Cloud drives are **import sources only**, never delivery origins: their direct links are temporary (OneDrive ~1 h), rate-limited (Google Drive) or capped by daily bandwidth (Dropbox).

## Consequences
- 0 MB at idle, support for 70+ providers for free.
- Dependency on rclone's CLI interface (pinned version, integration tests).
