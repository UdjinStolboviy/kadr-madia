# ADR-0004: Direct-to-storage uploads; Kadr not exposed to the internet

**Status:** Accepted

## Context
The initial idea was to embed tusd into Kadr for resumable uploads. But then browsers must reach Kadr, so Kadr would have to be published to the internet (often via a Cloudflare Tunnel with a 100 MB body limit and a ToS restricting video), loading the operator's uplink and widening the attack surface.

## Decision
- All client uploads go **directly to `kadr-private/tmp/`** via presigned PUT (≤ 64 MB) or S3 Multipart (> 64 MB, 16 MB parts).
- Resumability works at the multipart-part level: `@kadr/web` retries only failed parts and keeps upload state in IndexedDB to resume after a page reload.
- Kadr issues tickets to the product backend over the private network; the backend hands them to the browser.
- `/v1` listens on the private interface only. Only the admin console is published, behind Cloudflare Access (or a VPN).

## Consequences
- Zero media bytes pass through tunnels or product APIs; 25/100 MB body limits do not apply.
- Kadr is unreachable from the internet — a much smaller attack surface.
- Requires CORS on `kadr-private` and ETag handling in the client.
- tusd is not needed (fewer dependencies). It may return later for private-network clients only.

## Alternatives
- tusd in Kadr behind a tunnel — limits, ToS, uplink load.
- FormData POST — R2 does not support S3 POST policies.
