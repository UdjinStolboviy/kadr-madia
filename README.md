# Kadr

**A self-hosted media platform for photos and video: ingest, compress, store and deliver media for any number of products and tenants.**

> Self-hosted Cloudinary + Mux that runs as a single Go binary on a single small Linux host (reference deployment: a Proxmox LXC), stores files in Cloudflare R2, and is never on the read path.

---

## The problem

Every product reinvents uploads, compression, previews, private links and garbage collection. The result:

- camera originals of 4–8 MB (photos) and 60–150 MB (video) that we pay to store and serve;
- large files flowing through Cloudflare Tunnel and API servers (25/100 MB limits; the tunnel ToS forbids video);
- private documents that are easy to expose publicly by mistake;
- lock-in to Cloudinary, Mux or ImageKit pricing.

## What Kadr does

| Capability | How |
|---|---|
| **Direct-to-R2 uploads** | The browser uploads straight to R2 via presigned PUT / S3 Multipart. Zero media bytes pass through the tunnel or product APIs. |
| **Byte-budget compression** | Photos ≤ 20 KB (AVIF + WebP) with a quality/size search to hit the budget; PHOTO, DOCUMENT and BALANCED profiles. |
| **Video** | H.264 MP4 +faststart, CRF + bitrate ceiling, ≤ 15 KB poster, ThumbHash, VAAPI hardware encoding when an iGPU is present. |
| **Public and private** | Two physically separate buckets. Public files get permanent CDN URLs; private files get signed URLs verified by an edge Worker. |
| **Multi-tenancy** | Projects → namespaces (org/user) → assets. Keys, quotas, presets and webhooks are per project. |
| **Imports** | From URL (SSRF-safe) and from Google Drive / OneDrive / Dropbox (rclone on demand). |
| **Admin console** | Embedded in the binary (`go:embed`): projects, keys, storage, preset builder, queue, webhooks, savings analytics. |
| **SDKs** | `@kadr/node`, `@kadr/web` (pre-resize, multipart, progress), `@kadr/react` (`<KadrImage>` with ThumbHash). |

## Key properties

- **Kadr is not on the read path.** Views are served by Cloudflare CDN/R2. If Kadr is down, sites keep showing every image and video.
- **~60–90 MB RAM at idle** (no Docker, no Postgres, no Redis, no resident processing daemons).
- **$0 egress.** R2 egress is free; 1M photos at 20 KB ≈ 20 GB ≈ $0.15/month.
- **One file to deploy, one file of state.** `kadr` + `kadr.db` (continuously replicated to R2 with Litestream).

## Documentation

| Document | Contents |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Architecture, components, flows, diagrams |
| [docs/PRODUCT.md](docs/PRODUCT.md) | Product, positioning, competitors, business model, metrics |
| [docs/API.md](docs/API.md) | Public API, webhooks, signed URLs |
| [docs/ADMIN_API.md](docs/ADMIN_API.md) | Internal admin API contract |
| [docs/AGENTS.md](docs/AGENTS.md) | AI agents: MCP endpoint, permissions, approvals, moderation |
| [docs/DATA_MODEL.md](docs/DATA_MODEL.md) | Data model and SQLite schema |
| [docs/PIPELINES.md](docs/PIPELINES.md) | Photo and video processing pipelines, presets |
| [docs/SECURITY.md](docs/SECURITY.md) | Threat model, keys, encryption, isolation |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Host/LXC setup, systemd, R2, backups, monitoring, sizing |
| [docs/INTEGRATION.md](docs/INTEGRATION.md) | Integrating a product with Kadr |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Milestones, exit criteria, scaling path |
| [docs/adr/](docs/adr/) | Architecture Decision Records |

## Stack

Go 1.25 · chi · modernc SQLite (WAL, no CGO) · aws-sdk-go-v2 (S3/R2) · libvips CLI · ffmpeg/ffprobe · rclone · Litestream · React + Vite + Tailwind + shadcn (admin) · Cloudflare R2 + Workers · OpenAPI 3.1

## Repository layout

```
kadr/
├── cmd/kadr/              # entry point: serve | worker | migrate | admin | doctor
├── internal/
│   ├── config/            # config from env + /etc/kadr
│   ├── db/                # SQLite (writer + reader pool), migrations, queries
│   ├── auth/              # API keys, admin sessions, scopes
│   ├── storage/           # S3/R2/B2 drivers, presign, multipart
│   ├── engine/image/      # libvips: byte-budget encoding, ThumbHash
│   ├── engine/video/      # ffprobe/ffmpeg, VAAPI, posters
│   ├── pipeline/          # orchestration: upload → process → renditions → ready
│   ├── queue/             # lease-based job queue on SQLite
│   ├── webhook/           # outbox + Standard Webhooks
│   ├── delivery/          # signed URLs, key rotation
│   ├── importer/          # URL imports (SSRF-safe), rclone
│   ├── api/public/        # /v1 (for product backends)
│   ├── api/admin/         # /admin/api (for the admin console)
│   └── observability/     # slog, /metrics, health
├── web/                   # admin console (React), embedded in the binary
├── edge/kadr-edge/        # Cloudflare Worker for private delivery
├── sdk/{node,web,react}   # client SDKs (generated from OpenAPI)
├── api/openapi.yaml       # API contract, single source of truth
├── deploy/                # systemd units, LXC provisioning
└── docs/
```

## Local development

No cloud account, libvips or MinIO needed:

```sh
KADR_DEV=1 go run ./cmd/kadr migrate up
KADR_DEV=1 go run ./cmd/kadr admin create --email admin@example.com
KADR_DEV=1 go run ./cmd/kadr serve --dev-storage --fake-engine
```

- `KADR_DEV=1` uses `./var/` for the database, temp files and a generated master key; the public API listens on `127.0.0.1:8080`, the admin console on `127.0.0.1:8081`.
- `--dev-storage` (refused without `KADR_DEV=1`) starts an in-process S3-compatible server on `127.0.0.1:9000` (override with `KADR_DEV_STORAGE_ADDR`, loopback only) with the buckets `kadr-public` and `kadr-private`, stored in `var/devstorage/`. On first start it creates a platform storage profile named "Dev storage" pointing at it (credentials encrypted like any other profile; its address is updated if `KADR_DEV_STORAGE_ADDR` changes). Public renditions are served at `http://127.0.0.1:9000/kadr-public/<key>`, and presigned PUTs work from browsers (CORS allows any origin). It has no authentication — development only.
- `--fake-engine` replaces libvips with a deterministic engine that writes placeholder renditions. With libvips installed (`vips --version`), drop it to get real images: `KADR_DEV=1 go run ./cmd/kadr serve --dev-storage`.

Then create a project and an API key in the admin console and call the API, e.g. with `@kadr/node` (`sdk/node`).

Contract tests of the Node SDK against a real server (builds kadr, runs it in a temp dir with `--dev-storage --fake-engine`, exercises uploads, listing, presets, quotas, deletes and webhooks):

```sh
scripts/e2e-sdk.sh
```

## Status

M0 Foundation in progress (see [ROADMAP](docs/ROADMAP.md)).
