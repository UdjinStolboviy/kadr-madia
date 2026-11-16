# ADR-0006: Three buckets — public / private / system

**Status:** Accepted

## Context
We must guarantee that private files never become public through a code bug, and limit the blast radius of a leaked token.

## Decision
- `kadr-public` — public renditions only, Custom Domain.
- `kadr-private` — tmp sources, private renditions, originals; no public access.
- `kadr-system` — database replica; separate token, not available to the processing code path.
- Projects are separated by a `{project}/` prefix inside the buckets, not by separate buckets.

## Consequences
- A physical barrier between public and private; the public bucket has no write path from the Worker.
- No bucket creation per new project; Kadr enforces quotas and isolation.
- A project that needs full physical isolation gets a BYOS profile (its own buckets).

## Alternatives
- One bucket with public/private prefixes — a single Custom Domain/rule mistake exposes everything.
- Buckets per project — more setup and tokens without real benefit at S1.
