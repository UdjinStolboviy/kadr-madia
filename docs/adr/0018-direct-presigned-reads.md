# ADR 0018 — Private files read directly from the bucket (presigned GET)

**Status:** Accepted (2026-11-19). Preferred over ADR 0017 for installations whose uplink should not carry media.

## Context

Both private delivery modes so far proxy the bytes: kadr-edge (a Worker per Cloudflare account) and the delivery listener (ADR 0017, through the host's tunnel and uplink). Product backends and Kadr often share a LAN; the goal is that browsers fetch private files straight from storage and that neither Kadr nor the network it lives on carries them.

## Decision

- Private renditions can be read with **S3 SigV4 presigned GETs** on the provider endpoint.
- `POST /v1/signed-urls` returns them when no delivery host is configured (no signing key needed).
- `@kadr/node` gains a synchronous, dependency-free `presignGet` so a backend can sign locally with a **read-only** token scoped to the private bucket. That fits response interceptors that re-sign stored URLs on every response.
- Go (`storage.S3.PresignDownload`) and Node produce byte-identical URLs. A shared test vector pins this.
- URLs are signed with the start of a window `min(ttl, 1 h)` as the signing time and are valid for `ttl + window`. Within a window an object keeps one URL (browser cache hits); from any moment the URL stays valid for at least `ttl`.
- `download` maps to `response-content-disposition`. Range requests and the stored `Content-Type` / `Content-Disposition` / `Cache-Control` come from the provider. All of this was verified on R2.

## Consequences

- No Worker, no tunnel, no Kadr bandwidth; Kadr downtime does not affect reads.
- URLs show the provider host (`<account>.r2.cloudflarestorage.com`) and bucket name; custom domains are not supported for presigned R2 URLs.
- There is no edge cache, and each cache miss is a billed Class B read.
- At most 7 days per URL.
- Revocation is coarse: rotating the read-only token invalidates every URL it signed. The token must not have write access.
- Private HLS still needs a delivery host.
