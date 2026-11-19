# Security

## 1. Threat model (STRIDE, condensed)

| Threat | Vector | Mitigation |
|---|---|---|
| **Spoofing** | Stolen project API key | Keys live only in backends; LAN-only API; scopes; rotation; `last_used_at` + audit; instant revocation |
| | Forged private URL | HMAC-SHA256, mandatory `exp`, `kid`, constant-time comparison |
| | Forged webhook | Standard Webhooks HMAC + timestamp (5-minute window) |
| **Tampering** | File swapped between ticket and complete | Signed `Content-Length` and `Content-Type` in the presigned PUT; `HEAD` before processing; `sha256` check after download |
| **Repudiation** | Who changed a preset / deleted a key | `audit_log` for all admin actions and sensitive API calls |
| **Information disclosure** | Private file becomes public | Physically separate buckets; dedup never crosses visibility; the Worker has no write path to the public bucket |
| | GPS/EXIF leakage | Metadata is always stripped (vips strip, ffmpeg `-map_metadata -1`) |
| | Public URL enumeration | ULID blob_id (80 bits of entropy), no bucket listing |
| | Secrets leaked from the DB | Envelope encryption; the master key lives outside the DB and outside backups |
| **DoS** | "Bombs" (decompression, 100k×100k PNG, 10-hour video) | `maxInputBytes`, `maxInputPixels`, ffprobe limits before ffmpeg runs, process timeouts, pool semaphores, quotas |
| | Ticket flooding | Rate limits per key and namespace (token bucket), quota reservation |
| **Elevation of privilege** | AI agent exceeds its mandate (bug, prompt injection via file names / images / webhook payloads) | Separate agent tokens with level + explicit sensitive grants (owner only), project scope, per-route actions enforced in one middleware, approvals for destructive actions (default), deletion budget, rate limits, IP allowlist, expiry, global kill switch, reason + tool in every audit entry, `agent.denied` logging ([AGENTS.md](AGENTS.md)) |
| | RCE via a codec bug | Child processes without a shell (`exec.Command` with an argument array), dedicated unprivileged `kadr` user, systemd sandboxing, unprivileged LXC, libvips/ffmpeg updates |
| | SSRF via URL import | Resolve → check IP (RFC 1918, loopback, link-local, CGNAT, metadata, IPv6 ULA) → dial the verified IP; http/https only; limited redirects, each re-checked |

## 2. Keys and secrets

| Secret | Where it lives | Format |
|---|---|---|
| Master key (KEK) | `/etc/kadr/master.key` (0440, root:kadr), copy in a password manager | 32 bytes |
| API key pepper | `/etc/kadr/env` | 32 bytes |
| Project API keys | Only SHA-256(secret‖pepper) in the DB | `kd_live_<8-char prefix>_<32 base62 chars>` |
| URL signing secrets | `signing_keys.secret_enc` + Worker secrets | 32 bytes, `kid` |
| S3/R2 credentials, OAuth tokens, webhook secrets | `*_enc` in the DB | AES-256-GCM, per-record DEK, DEK wrapped by the KEK |
| Admin sessions | SHA-256 of the token in the DB | 32 bytes, cookie `__Host-kadr_session` |
| Agent tokens | Only SHA-256(secret‖pepper) in the DB | `kd_agent_<8-char prefix>_<32 base62 chars>`, shown once, rotatable |
| Held agent requests / results | `agent_approvals.body`, `result_body` | AES-256-GCM envelope, AAD bound to the approval id |

**Rotation:**
- `kadr keys rotate-master` — re-wraps every DEK with the new KEK (data untouched).
- URL signing key: new → `active`, old → `verify_only` for the max TTL (e.g. 24 h) → `retired`. The Worker receives both.
- API keys: several can be active at once for seamless replacement.

## 3. Network isolation

```
Internet ──► Cloudflare ──► Tunnel ──► kadr:8081 (/admin) — only via Cloudflare Access (email + MFA)
Internet ──► Cloudflare ──► Tunnel ──► kadr:8082 (delivery, optional) — HMAC-signed URLs only
Private network ──► kadr:8080 (/v1) — host firewall: allow only the product backends' IPs
kadr ──► R2 / webhooks / imports — outbound connections
```

- `/v1` is not published through the tunnel at all.
- The optional delivery listener (`KADR_DELIVERY_ADDR`) serves only GET/HEAD of objects whose URL carries a valid, unexpired HMAC signature of an active or verify-only key; the key's project must own the object (`{project_slug}/` prefix), retained originals are never served, and failures answer a bare `403` without the reason. It has no other routes besides `/healthz`. Do not let a Cache Rule ignore the query string on its hostname (OPERATIONS §7.1).
- Admin console: Cloudflare Access + own login (argon2id) + TOTP. Optional WebAuthn passkeys (discoverable, user-verified; counts as MFA; sign-count regression rejected; see ADMIN_API.md).
- Additionally, when `KADR_CF_ACCESS_TEAM_DOMAIN` and `KADR_CF_ACCESS_AUD` are set, the admin server verifies the `Cf-Access-Jwt-Assertion` header (RS256 against the team's published keys, issuer, audience, expiry) and refuses requests without it (`403 access_required`), except the exempt paths published with a Bypass policy (`KADR_CF_ACCESS_EXEMPT`). Without these variables the check is off — set them in production.
- MCP (`/mcp`) on the admin listener accepts only agent tokens; it rejects foreign browser `Origin`s (DNS rebinding). Behind Cloudflare Access use a service token or a path bypass restricted by IP plus the token's IP allowlist.

## 4. systemd sandbox

```ini
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=no            # /dev/dri is needed for VAAPI; without an iGPU → yes
DeviceAllow=/dev/dri/renderD128 rw
ReadWritePaths=/var/lib/kadr
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=no    # libx264/libaom use JIT assembly
SystemCallArchitectures=native
CapabilityBoundingSet=
```

## 5. Handling input files

1. Size is checked three times: at ticket creation (declared), by the R2 signature (`Content-Length`), and by `HEAD` before download.
2. The type is detected by magic bytes, never by extension or `Content-Type`.
3. Raster images: `vipsheader` → width × height ≤ `maxInputPixels` before any decoding.
4. Video: `ffprobe` with a 10 s timeout → duration, codecs, resolution, stream count.
5. Each job gets its own directory `/var/lib/kadr/tmp/<job_id>/`, removed in a `defer`; the sweeper cleans leftovers at startup.
6. Process timeouts: photo 30 s, video `duration × 4 + 60 s`.

## 6. Privacy and compliance

- **GDPR / right to erasure:** `DELETE /v1/namespaces/{ns}` and `DELETE /v1/assets/{id}`; physical deletion from R2 via GC (SLA ≤ 24 h), confirmed by an event.
- **Data minimization:** originals are not kept by default; EXIF/GPS are stripped.
- **Data residency:** R2 location hint EEUR/WEUR or the `eu` jurisdiction for EU-only.
- **Logs:** no file contents, no full signed URLs or secrets (slog redaction).

## 7. Supply chain

- `go mod verify`, `govulncheck` in CI; Renovate for dependencies.
- Reproducible builds (`-trimpath`, pinned Go), SHA-256 and artifact signing (cosign / minisign).
- `ffmpeg`, `libvips`, `rclone` from official Debian packages (security updates via `unattended-upgrades`).

## AI data flows

- AI moderation with Claude sends a downscaled preview (≤ 1024 px) of each moderated asset to the Anthropic API; scheduled agent runs send tool results (project metadata, analytics, previews the agent asks for). Nothing is sent while `ANTHROPIC_API_KEY` is unset. A self-hosted classifier (`KADR_AI_CLASSIFIER_URL`) keeps moderation images on your infrastructure.
- OAuth: public clients with mandatory PKCE (S256); codes are single-use, hashed, valid 2 minutes; refresh tokens are hashed and rotated on every use; redirect URIs must be pre-registered (https or loopback) and are matched exactly; consent requires an owner/admin session behind Cloudflare Access.

## Documents

Uploads of documents are validated by content (magic bytes, ZIP structure, UTF-8), never by name or declared type; HTML/SVG/XML, macro-enabled Office files, legacy binary Office files and archives are refused; zip bombs are bounded. Stored objects carry `Content-Disposition: attachment` and the real `Content-Type`. PDFs are rewritten only by sandboxed tools (`-dSAFER`, no shell, minimal environment, timeouts) and never when signed, encrypted or PDF/A; the rewrite is accepted only if the page count is unchanged. Malware scanning is not built in — put ClamAV in front if untrusted users upload documents that others will open.

