# Architecture Decision Records

Формат: Контекст → Рішення → Наслідки → Розглянуті альтернативи. Статуси: Proposed / Accepted / Superseded.

| # | Рішення | Статус |
|---|---|---|
| [0001](0001-go-chi.md) | Go + chi (net/http) | Accepted |
| [0002](0002-single-lxc-systemd.md) | Один unprivileged LXC, systemd, без Docker | Accepted |
| [0003](0003-sqlite-wal-queue.md) | SQLite WAL як БД і черга + Litestream | Accepted |
| [0004](0004-direct-to-r2-uploads.md) | Завантаження напряму в R2; Kadr не виставлений в інтернет; без tusd на публічному шляху | Accepted |
| [0005](0005-out-of-read-path.md) | Kadr поза шляхом читання: R2 Custom Domain + edge Worker | Accepted |
| [0006](0006-bucket-layout.md) | Три бакети: public / private / system | Accepted |
| [0007](0007-libvips-cli-image-engine.md) | libvips CLI замість демона imgproxy | Accepted |
| [0008](0008-eager-versioned-renditions.md) | Рендиції генеруються одразу за версіонованими пресетами | Accepted |
| [0009](0009-byte-budget-encoding.md) | Бюджетне кодування: пошук якості + зменшення розміру | Accepted |
| [0010](0010-video-h264-vaapi.md) | H.264 MP4 faststart за замовчуванням, VAAPI автодетект, HLS пізніше | Accepted |
| [0011](0011-webhooks-outbox.md) | Вебхуки через transactional outbox, Standard Webhooks | Accepted |
| [0012](0012-rclone-on-demand.md) | rclone як разовий процес для хмарних імпортів | Accepted |
| [0013](0013-single-account-byos.md) | Один легальний акаунт R2 + BYOS замість кількох безкоштовних акаунтів | Accepted |
