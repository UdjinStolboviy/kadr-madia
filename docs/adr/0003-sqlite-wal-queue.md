# ADR-0003: SQLite WAL as database and queue + Litestream

**Status:** Accepted

## Context
We need metadata storage (projects, assets, presets), a reliable job queue and a webhook outbox. Separate Postgres and Redis/Valkey mean +200–400 MB RAM and two more services to back up and upgrade.

## Decision
- `modernc.org/sqlite` (pure Go, no CGO), WAL, `synchronous=NORMAL`, one writer connection + a reader pool.
- The queue is a `jobs` table with atomic claiming (`UPDATE … RETURNING`) and `lease_until`.
- Litestream continuously replicates the WAL to `kadr-system` (R2).
- Data access only through the `Store` interface, with queries kept in one place per aggregate (sqlc-ready).

## Consequences
- ~5–10 MB RAM; backup and restore of a single file; RPO ≈ 1 s.
- Write throughput of hundreds to thousands of transactions/s — ample headroom for S1.
- A single control-plane instance. Workers scale horizontally via an internal lease API (S2); multiple control planes via a Postgres `Store` driver (S3).

## Alternatives
- Postgres + Valkey/BullMQ — overkill for one node.
- bbolt/BadgerDB — no SQL, harder analytics for the admin console.
