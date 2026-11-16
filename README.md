# Kadr

**Власна медіа-платформа для фото й відео: приймає, стискає, зберігає й видає медіафайли для всіх проєктів (omi, ofi, mif, далі — для зовнішніх клієнтів).**

> Self-hosted Cloudinary + Mux, що працює як один Go-бінарник в одному Proxmox LXC, зберігає файли в Cloudflare R2 і ніколи не стоїть на шляху читання.

---

## Яку проблему вирішує

Кожен продукт заново пише завантаження, стиснення, превʼю, приватні посилання й чистку сміття. У підсумку маємо:

- оригінали з камери по 4–8 МБ (фото) і 60–150 МБ (відео), за які платимо зберіганням і трафіком;
- великі файли, що йдуть через Cloudflare Tunnel та API-сервери (ліміти 25/100 МБ, ToS тунелю забороняє відео);
- приватні документи, які легко випадково зробити публічними;
- залежність від тарифів Cloudinary, Mux чи ImageKit.

## Що робить Kadr

| Можливість | Як |
|---|---|
| **Завантаження напряму в R2** | Браузер вантажить у R2 через presigned PUT / S3 Multipart. Через тунель і API проходить 0 байт медіа. |
| **Стиснення під бюджет байтів** | Фото ≤ 20 КБ (AVIF + WebP) із пошуком якості й розміру під заданий ліміт; профілі PHOTO, DOCUMENT, BALANCED. |
| **Відео** | H.264 MP4 +faststart, CRF + стеля бітрейту, постер ≤ 15 КБ, ThumbHash, апаратне кодування VAAPI, якщо є iGPU. |
| **Публічне й приватне** | Два фізично окремі бакети. Публічні файли — вічні CDN-URL; приватні — підписані URL, які перевіряє edge Worker. |
| **Мультитенантність** | Проєкти → простори імен (орг/юзер) → асети. Ключі, квоти, пресети, вебхуки окремо для кожного проєкту. |
| **Імпорт** | За URL (із захистом від SSRF) і з Google Drive / OneDrive / Dropbox (rclone на вимогу). |
| **Адмінка** | Вбудована у бінарник (`go:embed`): проєкти, ключі, сховища, конструктор пресетів, черга, вебхуки, статистика економії. |
| **SDK** | `@kadr/node`, `@kadr/web` (pre-resize, multipart, прогрес), `@kadr/react` (`<KadrImage>` із ThumbHash). |

## Ключові властивості

- **Kadr не стоїть на шляху читання.** Перегляди обслуговують Cloudflare CDN/R2. Якщо Kadr лежить, сайти й надалі показують усі медіа.
- **~60–90 МБ RAM у простої** (без Docker, без Postgres, без Redis, без постійних демонів обробки).
- **$0 за трафік.** У R2 egress безкоштовний; 1 млн фото по 20 КБ ≈ 20 ГБ ≈ $0.15/міс.
- **Один файл для деплою, один файл стану.** `kadr` + `kadr.db` (безперервна реплікація в R2 через Litestream).

## Документація

| Документ | Про що |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Архітектура, компоненти, потоки, діаграми |
| [docs/PRODUCT.md](docs/PRODUCT.md) | Продукт, позиціонування, конкуренти, бізнес-модель, метрики |
| [docs/API.md](docs/API.md) | Публічний API, вебхуки, підписані URL |
| [docs/DATA_MODEL.md](docs/DATA_MODEL.md) | Модель даних і схема SQLite |
| [docs/PIPELINES.md](docs/PIPELINES.md) | Пайплайни обробки фото й відео, пресети |
| [docs/SECURITY.md](docs/SECURITY.md) | Модель загроз, ключі, шифрування, ізоляція |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Proxmox LXC, systemd, R2, бекапи, моніторинг, ресурси |
| [docs/INTEGRATION.md](docs/INTEGRATION.md) | Підключення omi / ofi / mif |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Етапи, критерії готовності, шлях масштабування |
| [docs/adr/](docs/adr/) | Архітектурні рішення (ADR) з обґрунтуванням |

## Стек

Go 1.25 · chi · modernc SQLite (WAL, без CGO) · sqlc · aws-sdk-go-v2 (S3/R2) · libvips CLI · ffmpeg/ffprobe · rclone · Litestream · React + Vite + Tailwind + shadcn (адмінка) · Cloudflare R2 + Workers · OpenAPI 3.1

## Структура репозиторію

```
kadr/
├── cmd/kadr/              # точка входу: serve | worker | migrate | admin | keys
├── internal/
│   ├── config/            # конфіг з env + /etc/kadr
│   ├── db/                # SQLite (writer + reader pool), міграції, sqlc
│   ├── auth/              # API-ключі, сесії адмінки, scopes
│   ├── storage/           # драйвери S3/R2/B2, presign, multipart
│   ├── engine/image/      # libvips: бюджетне кодування, ThumbHash
│   ├── engine/video/      # ffprobe/ffmpeg, VAAPI, постери
│   ├── pipeline/          # оркестрація: upload → process → renditions → ready
│   ├── queue/             # черга задач на SQLite з lease
│   ├── webhook/           # outbox + Standard Webhooks
│   ├── delivery/          # підписані URL, ротація ключів
│   ├── importer/          # URL-імпорт (SSRF-safe), rclone
│   ├── api/public/        # /v1 (для бекендів проєктів)
│   ├── api/admin/         # /admin/api (для адмінки)
│   └── observability/     # slog, /metrics, health
├── web/                   # адмінка (React), збирається в бінарник
├── edge/kadr-edge/        # Cloudflare Worker для приватної видачі
├── sdk/{node,web,react}   # клієнтські SDK (генеруються з OpenAPI)
├── api/openapi.yaml       # контракт API, єдине джерело правди
├── deploy/                # systemd-юніти, налаштування LXC
└── docs/
```

## Статус

Етап проєктування. Наступний крок — M0 Foundation (див. [ROADMAP](docs/ROADMAP.md)).
