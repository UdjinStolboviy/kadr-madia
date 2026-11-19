# ADR 0017 — Private delivery from Kadr through a tunnel (no Worker)

**Status:** Accepted (2026-11-19)

## Context

Private renditions are served by signed URLs that the `kadr-edge` Worker verifies. Every Cloudflare account needs its own Worker deployment, an R2 binding and published signing keys. For small installations this is extra infrastructure, and a Worker cannot read storage outside R2 bindings (B2, Hetzner, Garage).

Alternatives considered: R2 presigned SigV4 URLs (no custom domain, no edge cache, coarse revocation, no private HLS) and WAF token authentication (paid plans, Cloudflare's own signature format, no per-URL `download`, no HLS).

## Decision

Kadr gets an optional third listener, `KADR_DELIVERY_ADDR`, that verifies the **same** URLs as kadr-edge (`GET\n/{key}\n{exp}\n{download}`, and `GETP` directory signatures for HLS) and streams the object from the project's storage profile. It is published through the existing Cloudflare Tunnel; no port is opened. One listener serves all projects:

- `kid` → signing key (active or verify_only) → project → storage profile → private bucket;
- a key is served only under `{project_slug}/` (a valid signature of another project, or of a retained original, is refused);
- resolved keys are cached for 30 s (misses for 10 s), so retiring a key takes effect within 30 s and random kids do not reach the database;
- concurrency is bounded (`KADR_DELIVERY_MAX_STREAMS`, 503 beyond it); responses extend their write deadline per chunk instead of a fixed timeout.

The delivery mode is chosen per storage profile by its `private_base_url` (Worker host or tunnel host); SDKs and clients do not change.

## Consequences

- No Worker or key publishing is needed; it works with every S3-compatible provider.
- Private media depends on Kadr's availability and the host's uplink. The Worker stays the recommended option for high-traffic video.
- Edge caching is opt-in (`KADR_DELIVERY_CDN_MAX_AGE`) and safe only with the query string in the cache key; it pays off only when clients reuse URLs (rounded `exp`).
