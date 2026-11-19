# Architecture Decision Records

Format: Context → Decision → Consequences → Alternatives considered. Statuses: Proposed / Accepted / Superseded.

| # | Decision | Status |
|---|---|---|
| [0001](0001-go-chi.md) | Go + chi (net/http) | Accepted |
| [0002](0002-single-lxc-systemd.md) | Single unprivileged LXC/host, systemd, no Docker | Accepted |
| [0003](0003-sqlite-wal-queue.md) | SQLite WAL as database and queue + Litestream | Accepted |
| [0004](0004-direct-to-r2-uploads.md) | Direct-to-storage uploads; Kadr not exposed to the internet; no tusd on the public path | Accepted |
| [0005](0005-out-of-read-path.md) | Kadr off the read path: R2 Custom Domain + edge Worker | Accepted |
| [0006](0006-bucket-layout.md) | Three buckets: public / private / system | Accepted |
| [0007](0007-libvips-cli-image-engine.md) | libvips CLI instead of an imgproxy daemon | Accepted |
| [0008](0008-eager-versioned-renditions.md) | Eager renditions from versioned presets | Accepted |
| [0009](0009-byte-budget-encoding.md) | Byte-budget encoding: quality search + downscaling | Accepted |
| [0010](0010-video-h264-vaapi.md) | H.264 MP4 faststart by default, VAAPI auto-detection, HLS later | Accepted |
| [0011](0011-webhooks-outbox.md) | Webhooks via transactional outbox, Standard Webhooks | Accepted |
| [0012](0012-rclone-on-demand.md) | rclone as a one-off process for cloud imports | Accepted |
| [0013](0013-single-account-byos.md) | One legitimate platform storage account + BYOS | Accepted |
| [0014](0014-agent-access-mcp.md) | AI agent access through MCP on top of the Admin API | Accepted |
| [0015](0015-ai-operations.md) | AI operations: Claude moderation and scheduled agent runs | Accepted |
| [0016](0016-storage-key-layout.md) | Descriptive, partitioned storage keys | Accepted |
| [0017](0017-origin-delivery.md) | Private delivery from Kadr through a tunnel (no Worker) | Accepted |
