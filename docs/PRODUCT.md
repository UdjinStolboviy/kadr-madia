# Kadr: Product

## One sentence

**Kadr is media infrastructure for product teams: uploads, byte-budget compression, and private and public delivery of photos and video at object-storage prices, with zero egress fees.**

## The problem

1. **Media is the most expensive and heaviest part of user-generated content.** Raw phone photos weigh 4–8 MB, video 60–150 MB per 30 seconds.
2. **Managed services get expensive at scale.** Cloudinary, ImageKit and Uploadcare charge for transformations and bandwidth; Mux charges for encoding, storage and delivery minutes. AWS S3 egress costs ~$0.09/GB.
3. **Home-grown solutions are rebuilt in every product** and often have holes: files flow through API servers, private documents are reachable via public URLs, nobody cleans up garbage.

## The solution

One service that:
- ingests files directly into object storage (browser → R2) without loading the product's API;
- compresses to a **byte budget** or a **perceptual quality target** — photos ≤ 20 KB that stay sharp;
- delivers public files through a CDN and private files through edge-verified signed links;
- **is not on the read path**: a Kadr outage does not affect views;
- runs on minimal hardware (one Go binary, ~60–90 MB RAM at idle).

## Differentiation

| | Kadr | Cloudinary / ImageKit | Mux | Uploadcare | DIY on S3 |
|---|---|---|---|---|---|
| Egress fees | **$0** (R2) | per GB / credits | per delivery minute | per GB | ~$0.09/GB (AWS) |
| Byte-budget compression (≤ N KB) | **yes, guaranteed** | partial (auto-quality) | — | partial | manual |
| Photo + video in one API | yes | yes | video only | yes | manual |
| Private files | separate bucket + edge HMAC | signed URLs | signed playback | signed URLs | often missing |
| Self-hosted / own storage | **yes (BYOS)** | no | no | no | yes |
| Outage affects views | **no** | yes (their CDN) | yes | yes | depends |
| Vendor lock-in | none (S3 API) | high | high | high | none |

## Economics

| Volume | Raw files | After Kadr | R2/month |
|---|---|---|---|
| 1M photos | ~5 TB | ~20 GB (photo-lite) | ~$0.15 |
| 10k videos × 30 s | ~1 TB | ~25 GB (720p) | ~$0.23 |
| 10 TB/month traffic | — | — | **$0** egress |

Cost for a product = storage of the results + a share of the LXC. Compared with storing originals, storage savings are **95–99%** and bandwidth savings 100%.

## Product path

1. **Design partners (now):** run Kadr in production for a small set of products, prove reliability, collect savings and performance metrics.
2. **Open-core self-hosted:** Community (free: photos, MP4 video, single node) + Pro license (HLS, GPU, remote workers, SSO, audit, priority support). Target audience: studios, agencies, SaaS teams on Proxmox/VPS.
3. **Managed Kadr Cloud:** hosted control plane + workers, BYOS or platform R2. Model: flat plan + metering (processed GB, video minutes), **no delivery fees**.

## Metrics for investors

| Metric | Target |
|---|---|
| Storage savings (bytes in → bytes stored) | ≥ 95% |
| Photo processing p95 (complete → ready) | < 1.5 s |
| 30 s video processing p95 (CPU / VAAPI) | < 25 s / < 6 s |
| Delivery availability | = Cloudflare SLA (Kadr off the read path) |
| Failed processing rate | < 0.5% |
| Cost per 1,000 active assets/month | < $0.01 |
| Time to onboard a new product | < 1 day |

## Risks and answers

- **"Cloudflare will change pricing"** → storage is behind the S3 API; switching to B2/Hetzner/Garage is configuration, not code.
- **"Why not Cloudinary?"** → 10–100× cheaper at scale, no lock-in, data stays in our account.
- **"Single-node architecture won't scale"** → the S1 → S2 (remote workers) → S3 (Postgres, multi-region) path is built into the interfaces from day one ([ARCHITECTURE.md §8](ARCHITECTURE.md)).
