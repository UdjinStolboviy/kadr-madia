# ADR-0005: Kadr off the read path

**Status:** Accepted

## Context
Proxying views through the Kadr host destroys the benefits of R2 (zero egress, global CDN), creates a single point of failure, and violates tunnel ToS for video.

## Decision
- **Public:** `kadr-public` with Custom Domain `cdn.<domain>`, Cache Everything, `immutable`. The browser picks the format via `<picture>` (AVIF → WebP), so no Worker is needed on the public path.
- **Private:** Worker `kadr-edge` on `media.<domain>` with a read binding to `kadr-private`. It verifies HMAC (`kid`, `exp`), caches via the Cache API with a signature-free cache key, and supports Range requests for video.
- Signatures are generated locally by the SDK in the product backend.

## Consequences
- A Kadr outage does not affect views; delivery availability equals the Cloudflare SLA.
- Workers free plan: 100k requests/day, for private files only; beyond that Workers Paid ($5/month) or presigned GET as a fallback.
- A specific private link cannot be revoked before `exp` (the trade-off of stateless signatures). TTLs are therefore short (≤ 1 h), and an incident is handled by rotating the `kid`.

## Alternatives
- Presigned GET directly from the R2 S3 endpoint — not CDN-cacheable, long URLs; kept as a fallback.
- A Worker on the public path for `f_auto` — extra invocations and cost.
