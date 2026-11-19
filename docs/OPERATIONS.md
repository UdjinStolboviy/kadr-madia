# Operations

## 1. Proxmox LXC

| Parameter | Value |
|---|---|
| Template | Debian 13 (trixie), **unprivileged** |
| CPU | 2 vCPU (start) → 4 vCPU (regular video without an iGPU) |
| RAM | 2048 MB + 512 MB swap |
| Disk | 30 GB on NVMe storage (OS ~2 GB, the rest is tmp for video) |
| Network | Static private IP, DNS name e.g. `kadr.internal` |
| Features | `nesting=0`, `keyctl=0` |
| Autostart | `onboot: 1`, `startup: order=3` (after network/DB hosts) |

### Intel iGPU passthrough (VAAPI), optional

`/etc/pve/lxc/<ID>.conf`:

```ini
dev0: /dev/dri/renderD128,gid=993,mode=0660
```

(Proxmox 8.2+; for older versions use `lxc.cgroup2.devices.allow: c 226:128 rwm` + `lxc.mount.entry`.) Inside the LXC: the `kadr` user in the `render` group, the `intel-media-va-driver` package, verify with `vainfo`.

## 2. Packages

```bash
apt update && apt install -y --no-install-recommends \
  libvips-tools ffmpeg rclone ca-certificates tzdata \
  intel-media-va-driver vainfo   # only with an iGPU
# Litestream — official .deb from GitHub releases (pinned version + SHA-256 check)
useradd --system --home /var/lib/kadr --shell /usr/sbin/nologin kadr
install -d -o kadr -g kadr -m 0750 /var/lib/kadr /var/lib/kadr/tmp /var/lib/kadr/db
install -d -o root -g kadr -m 0750 /etc/kadr
```

AVIF support in vips: `vips heifsave` must support `compression=av1` (libheif + libaom/svt). `kadr doctor` checks all of this automatically. `deploy/lxc/provision.sh` automates this section.

## 3. File layout

```
/opt/kadr/releases/<version>/kadr   # binary with the embedded admin console
/opt/kadr/current -> releases/<version>
/etc/kadr/env                       # config (0640 root:kadr)
/etc/kadr/master.key                # KEK (0440 root:kadr)
/etc/kadr/litestream.yml
/var/lib/kadr/db/kadr.db            # + -wal, -shm
/var/lib/kadr/tmp/<job_id>/         # per-job working directories
```

## 4. Config `/etc/kadr/env`

```bash
KADR_PUBLIC_ADDR=10.0.0.20:8080          # /v1, private interface only
KADR_ADMIN_ADDR=127.0.0.1:8081           # admin, exposed via cloudflared
KADR_DB_PATH=/var/lib/kadr/db/kadr.db
KADR_TMP_DIR=/var/lib/kadr/tmp
KADR_MASTER_KEY_FILE=/etc/kadr/master.key
KADR_API_KEY_PEPPER=…
KADR_POOL_IMAGE=2
KADR_POOL_VIDEO=1
KADR_POOL_FILE=1                         # document jobs (PDF optimisation)
KADR_VIDEO_HW=auto                       # auto | vaapi | off
KADR_VIDEO_AV1_LP=0                      # SVT-AV1 parallelism for AV1 presets; 2 on hosts with ≤ 2 GB RAM
KADR_EDGE_BASE_URL=https://media.example.com   # private delivery host (kadr-edge or the delivery listener) for POST /v1/signed-urls
KADR_DELIVERY_ADDR=0.0.0.0:8082          # signed private delivery from Kadr itself (origin mode, §7.1); empty = off
KADR_DELIVERY_MAX_STREAMS=64             # concurrent delivery responses; beyond it 503 + Retry-After
KADR_DELIVERY_CDN_MAX_AGE=0              # seconds Cloudflare may cache a signed response (see §7.1 before enabling)
KADR_WEBAUTHN_RP_ID=admin.example.com           # passkeys: relying party id (host of the admin UI)
KADR_WEBAUTHN_ORIGINS=https://admin.example.com # passkeys: allowed origins, comma separated
KADR_TELEGRAM_BOT_TOKEN=…                # built-in alerts (optional)
KADR_TELEGRAM_CHAT_ID=…
KADR_CF_ACCOUNT_ID=…                     # to publish keys to kadr-edge
KADR_CF_API_TOKEN=…                      # scope: Workers Scripts:Edit (this Worker only)
KADR_ADMIN_PUBLIC_URL=https://admin.example.com # external admin URL: MCP/OAuth issuer, setup snippets, notification links
KADR_CF_ACCESS_TEAM_DOMAIN=myteam.cloudflareaccess.com # verify Cloudflare Access JWTs on the admin listener (optional)
KADR_CF_ACCESS_AUD=…                     # Access application AUD tag(s), comma separated
# KADR_CF_ACCESS_EXEMPT=/healthz,/readyz,/mcp,/admin/mcp,/oauth/token,/oauth/register,/.well-known/
ANTHROPIC_API_KEY=…                      # AI moderation (Claude vision) and scheduled agent runs (optional)
# KADR_AI_MODEL=claude-opus-5-5
# KADR_AI_CLASSIFIER_URL=http://127.0.0.1:9100/classify  # self-hosted moderation classifier instead of Claude
KADR_LOG_LEVEL=info
```

R2 credentials are not stored here but encrypted in the DB (`storage_profiles`) — they are entered in the admin console.

## 5. systemd

`deploy/systemd/kadr.service`, `deploy/systemd/litestream.service`. First start:

```bash
kadr migrate up
kadr admin create --email you@domain
systemctl enable --now litestream kadr
```

## 6. Deploying from a Mac

```bash
make release VERSION=0.3.0      # web build → go build (CGO_ENABLED=0 GOOS=linux GOARCH=amd64 -trimpath -ldflags "-s -w -X main.version=…")
make deploy  HOST=kadr.internal      # scp → /opt/kadr/releases/0.3.0 → kadr migrate up → ln -sfn → systemctl restart → /readyz
make rollback HOST=kadr.internal     # ln -sfn to the previous version → restart
```

**Graceful restart:** on SIGTERM Kadr stops claiming jobs, waits for photo jobs to finish (≤ 30 s), returns video jobs to the queue (lease reset), and closes HTTP. `TimeoutStopSec=60`.

Migrations are forward-only and backward compatible with the previous version (expand → migrate → contract), so a binary rollback is always safe.

## 7. Cloudflare R2 setup

1. Buckets `kadr-public`, `kadr-private`, `kadr-system`, location hint **EEUR**.
2. `kadr-public` → Custom Domain `cdn.<domain>`; Cache Rule: Cache Everything, Edge TTL 1 year.
3. `kadr-private` → Public access **OFF**; CORS:
   ```json
   [{ "AllowedOrigins": ["https://app.example.com","https://shop.example.com","http://localhost:3000"],
      "AllowedMethods": ["PUT","HEAD"], "AllowedHeaders": ["content-type","content-length","x-amz-*"],
      "ExposeHeaders": ["ETag"], "MaxAgeSeconds": 3600 }]
   ```
   Lifecycle: delete `tmp/` after 1 day; abort incomplete multipart after 1 day.
4. `kadr-system` → lifecycle: delete noncurrent versions after 30 days.
5. API tokens: `kadr-rw` (Object R/W on public + private), `kadr-backup` (Object R/W on system only).
6. Private delivery, one of:
   - Worker `kadr-edge` on `media.<domain>` with an R2 binding `PRIVATE` (read) and signing-key secrets; or
   - the delivery listener of Kadr published through the tunnel (§7.1) — no Worker.
7. Billing alert at $5.

### 7.1 Private delivery without a Worker (origin mode)

Kadr can verify signed URLs and stream private objects itself (ADR 0017). One listener serves every project: the `kid` names the project, the project's storage profile names the bucket, and only keys under `{project_slug}/` are served.

1. `/etc/kadr/env`: `KADR_DELIVERY_ADDR=0.0.0.0:8082` (or the LAN address the tunnel connector reaches), restart Kadr; `curl http://<host>:8082/healthz` → `ok`.
2. Cloudflare Zero Trust → Networks → Tunnels → the tunnel → Public hostname: `media.<domain>` → `http://<kadr-host>:8082`. **No Cloudflare Access application** on this hostname: the HMAC signature is the authorization. The zone must belong to the tunnel's account; remove a Worker custom domain on the same hostname first.
3. Admin → Storage → the project's profile → `private_base_url` = `https://media.<domain>` (or `KADR_EDGE_BASE_URL`). Signing keys need no publishing: rotations and retirements apply within 30 s.
4. Check: `POST /v1/signed-urls` → open the URL (200), change one character of `sig` (403), `curl -H 'Range: bytes=0-99'` (206).

Behaviour matches kadr-edge: `Range`/`If-Range`, `If-None-Match` → 304, `download` → `Content-Disposition`, the stored disposition of documents, `nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: private, max-age ≤ 1 h` (capped by the URL's validity), HLS directory signatures with playlist rewriting. Metrics: `kadr_delivery_requests_total{status}`, `kadr_delivery_bytes_total`.

**Trade-offs.** Every byte goes R2 → Kadr → tunnel → visitor, so the host's uplink and availability now limit private media (a Kadr restart interrupts playback; the Worker keeps serving while Kadr is down). `KADR_DELIVERY_MAX_STREAMS` keeps delivery from starving the media workers.

**Edge caching (`KADR_DELIVERY_CDN_MAX_AGE`).** When set, responses carry `Cloudflare-CDN-Cache-Control: max-age=…` so Cloudflare can cache them. This is safe only while the cache key contains the **full query string** (the default): a Cache Rule that ignores the query string would serve a cached private object to anyone who knows its path, without a signature. Caching only pays off when clients reuse the same URL — round `exp` up (e.g. to the hour) when signing so one object keeps one URL within that window.

## 8. Backup and restore

| What | How | RPO / RTO |
|---|---|---|
| `kadr.db` | Litestream → `kadr-system` (1 s sync, daily snapshot, 30-day retention) | RPO ≈ 1 s / RTO ≈ 5 min |
| Media | Lives in R2 (eleven nines of durability). For critical projects — nightly replication of `kadr-public`/`kadr-private` → Backblaze B2 via `rclone sync` | RPO 24 h |
| Master key | Password manager + offline copy | — |
| LXC | Weekly Proxmox vzdump (optional: everything is restorable from the binary + Litestream) | — |

**Restore from scratch:** new LXC → packages → binary → `/etc/kadr/*` → `litestream restore -o /var/lib/kadr/db/kadr.db s3://kadr-system/…` → `systemctl start kadr`. Monthly test restore: `make restore-drill HOST=…` runs `kadr restore-drill` (litestream restore into a scratch dir, `PRAGMA integrity_check`, migration count and table row counts compared with the live DB; exits non-zero on failure).

## 9. Monitoring

- `/healthz` (alive), `/readyz` (DB + R2 + vips/ffmpeg available).
- `/metrics` (Prometheus format, LAN only): `kadr_jobs_queued{pool}`, `kadr_job_duration_seconds{kind}`, `kadr_job_failures_total{kind,code}`, `kadr_bytes_in_total`, `kadr_bytes_stored_total`, `kadr_r2_requests_total{op,status}`, `kadr_webhook_deliveries_total{status}`, `kadr_tmp_disk_free_bytes`, `kadr_db_wal_bytes`, `kadr_video_transcodes_total{encoder}`, `kadr_video_hw_fallbacks_total`, `kadr_video_transcode_media_seconds_total{encoder}` / `kadr_video_transcode_wall_seconds_total{encoder}` (their ratio is the encode speed × realtime).
- Video encoder: every transcode logs `video transcoded` with `encoder`, `hw_requested`, `hw_fallback`, `speed_x`, `elapsed_ms`, `cpu_ms`; a VAAPI → libx264 fallback is a `WARN` with `hw_error`. The encoder is also stored per rendition (`renditions.encoder`) and returned in the asset JSON. Check: `journalctl -u kadr -o cat | jq 'select(.msg=="video transcoded") | {asset_id, encoder, hw_fallback, speed_x}'`. Alert on `increase(kadr_video_hw_fallbacks_total[1h]) > 0`.
- Built-in alerts (Telegram when `KADR_TELEGRAM_BOT_TOKEN`/`KADR_TELEGRAM_CHAT_ID` are set; checked every minute, 30 min cooldown, "recovered" message): video queue > 10, oldest queued job > 15 min, ≥ 3 dead jobs in 15 min, tmp disk < 2 GiB. Further rules via Grafana:
  - video queue > 10 jobs or oldest > 15 min;
  - failed share > 5% over 15 min;
  - tmp free space < 5 GB;
  - Litestream lag > 60 s;
  - dead webhooks > 0.
- AI: `kadr_ai_moderations_total{status}`, `kadr_ai_moderation_errors_total`, `kadr_ai_moderation_seconds`, `kadr_ai_agent_runs_total{status}`; jobs `ai.moderate` / `ai.agent_run` run in pool `ai` (concurrency 2), `ai.tick` every minute in `maintenance`.
- AI agents: a pending approval sends a Telegram message (same bot); agent actions are in the audit log (`actor_type=agent`). Emergency stop: admin → AI agents → turn off agent access.
- Logs: JSON in journald (`journalctl -u kadr -o cat | jq`), `request_id`/`job_id`/`asset_id` on every record.

## 10. Capacity planning

| Monthly load | Photos (photo-lite) | 30 s videos (720p) | CPU hours | R2 storage (cumulative) |
|---|---|---|---|---|
| Start | 50,000 | 500 | ~3 | ~1 GB + ~1 GB |
| Growth | 500,000 | 5,000 | ~25 | ~10 GB + ~10 GB |
| Scale | 5,000,000 | 50,000 | ~250 → needs S2 workers / VAAPI | ~100 GB + ~100 GB → ~$3/month |

Proxmox uplink: every video pulls its source from R2 (50–150 MB) and pushes ~2–3 MB back; 5,000 videos/month ≈ 500 GB of inbound traffic on the home/VPS connection — account for this in the ISP plan.


## First administrator (generated on deploy)

`make deploy HOST=… VERSION=… [ADMIN_URL=https://admin.example.com] [ADMIN_EMAIL=you@example.com]` runs `kadr admin bootstrap` after the migrations. On the **first** deploy (no administrator exists) it creates an `owner` with a random 24-character password and prints a box in the deploy console:

```
================================================================
  KADR ADMIN CONSOLE — FIRST LOGIN (shown once, save it now)
================================================================
  URL:       https://admin.example.com
  Email:     admin@kadr.local
  Password:  …
================================================================
```

The password is shown only there (it is hashed with argon2id and not stored or logged anywhere else). Later deploys print `administrator already exists; no credentials generated`. The default email is `admin@kadr.local`; override with `ADMIN_EMAIL` / `KADR_BOOTSTRAP_ADMIN_EMAIL`. Lost the password? Create another owner with `kadr admin create --email … --password-stdin` on the host. Change the password and enable TOTP or a passkey after the first sign-in.

**Admin console over plain HTTP.** On a LAN address (`http://192.168.x.x:8081`) browsers drop `Secure` / `__Host-` cookies, which made the login succeed and the next request bounce back to the login page. Kadr now chooses the cookie per request: over HTTPS (TLS, `X-Forwarded-Proto: https` or Cloudflare's `CF-Visitor`) it uses the hardened `__Host-kadr_session`; over plain HTTP it uses a non-Secure `kadr_session`. Use HTTPS (cloudflared) for anything but a trusted network.


## Documents: packages

Document upload works without extra packages (validation + storage). For PDF optimisation and thumbnails install `ghostscript` and `qpdf` (`apt-get install ghostscript qpdf`; `deploy/lxc/provision.sh` does it). System → capabilities and `kadr doctor` list them as optional. Ghostscript has a history of security issues: keep it updated (`unattended-upgrades`); Kadr runs it with `-dSAFER`, a minimal environment, `nice` and per-command timeouts, inside the systemd sandbox.
