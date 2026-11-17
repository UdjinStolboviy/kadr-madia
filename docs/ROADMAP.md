# Roadmap

Estimates assume one full-time engineer working with AI agents.

## M0 — Foundation (1–2 weeks)

- Skeleton: `cmd/kadr` (serve, migrate, admin, doctor), config, slog, graceful shutdown.
- SQLite: writer + reader pool, embedded migrations, `Store` interface.
- Envelope encryption, API keys, admin sessions (argon2id + TOTP).
- Projects, storage profiles (+ "Test connection" against R2), default presets.
- Admin console: shell (React + Vite + shadcn), login, Projects, Keys, Storage.
- CI: lint, test, govulncheck, linux/amd64 cross build; `make deploy` / `rollback`.

**Done when:** `kadr doctor` is green on the LXC, and the admin console can create an `acme-staging` project with a key and a working R2 profile.

## M1 — Images (2–3 weeks)

- `POST /v1/uploads` (PUT), `complete?wait`, `POST /v1/assets` (server-side upload).
- SQLite queue (leases, retries, DLQ), `image` pool.
- `engine/image`: vips CLI, budget encoding, AVIF+WebP, ThumbHash, dominant color.
- sha256 dedup, refcount GC, expired-upload sweeper.
- Outbox + webhooks (Standard Webhooks).
- Project and namespace quotas with reservations.
- `@kadr/node` (tickets, signUrl, webhook verification).
- **First design-partner product integrated behind a feature flag** (staging).

**Done when:** an iPhone photo (4 MB HEIC) → ≤ 20 KB AVIF in R2 with p95 < 1.5 s from complete; golden tests green.

## M2 — Video (2 weeks) — done

- S3 Multipart (part tickets, complete with ETags), abort.
- `engine/video`: ffprobe validation, libx264 / VAAPI auto-detection, poster, progress.
- `video` pool with heartbeat leases; duration-based timeouts.
- Monthly video-minute quota.
- Jobs page in the admin console (live progress via SSE, Retry/Cancel).

**Done when:** a 60-second 4K 300 MB video uploads bypassing the tunnel, compresses to ≤ 5 MB 720p, and plays instantly (faststart).

## M3 — Private delivery & SDKs (1–2 weeks) — done except visibility change

- `kadr-edge` Worker: HMAC verification, Cache API, Range requests for video, `Content-Disposition`.
- Signing keys: creation, rotation, publishing to the Worker via the Cloudflare API.
- Visibility changes (CopyObject between buckets).
- `@kadr/web` (pre-resize in a Web Worker, parallel multipart, part retries, progress), `@kadr/react` (`<KadrImage>`, `<KadrVideo>`).
- OpenAPI 3.1 published; SDKs generated.
- **Onboard two more design-partner products; integration guide validated (< 1 day per product).**

## M4 — Product polish (2–3 weeks) — done

- Asset explorer (filters, metadata view, deletion).
- **Playground:** upload a sample, tweak preset parameters, Before/After split view, live size/SSIM, "Save as new version".
- Analytics: ingested / stored / savings %, video minutes, failed rate, p95 — per project and day.
- Webhook delivery log with re-send; audit log.
- URL imports (SSRF-safe) and cloud connections (Google Drive / OneDrive / Dropbox via rclone).
- Litestream + `restore-drill`; `/metrics`, built-in alerts (Telegram/email).
- WebAuthn passkeys for the admin console (`KADR_WEBAUTHN_RP_ID` / `KADR_WEBAUTHN_ORIGINS`).

## M5 — Scale & SaaS-ready (as needed)

- `kadr worker` on separate nodes (internal lease API, mTLS); GPU node with NVENC/QSV.
- HLS ladder, timeline sprites, AV1 renditions.
- On-demand renditions (lazy, cached in R2) — or imgproxy behind a Worker.
- Postgres driver for `Store` (multi-instance control plane).
- Billing: metering from `usage_daily` → Stripe; plans.
- Content moderation (NSFW classifier as a separate pool), document checks.
- Self-hosted package: `.deb`, Proxmox installer (helper script), customer documentation.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| SQLite limits as writes grow | API latency | Single writer + batching; Postgres driver in M5; the threshold (hundreds of writes/s) is far beyond S1 needs |
| Proxmox uplink for pulling video from R2 | Video queue grows | VAAPI, S2 workers on a VPS/Hetzner close to R2 |
| R2 terms change (pricing, ToS) | Cost | `StorageDriver` abstraction; B2/Hetzner/Garage as drop-in replacements without API changes |
| ≤ 20 KB quality on complex photos | User dissatisfaction | `photo-balanced` alternative, `over_budget` flag, Playground for tuning |
| Bus factor = 1 | Maintainability | ADRs, OpenAPI, tests, runbooks in OPERATIONS.md |
