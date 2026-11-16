# Дорожня карта

Оцінки — для однієї людини, яка працює над проєктом повний день, з допомогою AI-агентів.

## M0 — Foundation (1–2 тижні)

- Скелет: `cmd/kadr` (serve, migrate, admin, doctor), конфіг, slog, graceful shutdown.
- SQLite: writer + reader pool, міграції (embed), sqlc, `Store`-інтерфейс.
- Envelope-шифрування, API-ключі, сесії адмінки (argon2id + TOTP).
- Проєкти, storage profiles (+ кнопка «Тест з'єднання» до R2), стандартні пресети.
- Адмінка: каркас (React + Vite + shadcn), логін, Projects, Keys, Storage.
- CI: lint, test, govulncheck, кросзбірка linux/amd64; `make deploy` / `rollback`.

**Готово, коли:** `kadr doctor` зелений на LXC, у адмінці створюється проєкт `omi-staging` з ключем і робочим R2-профілем.

## M1 — Images (2–3 тижні)

- `POST /v1/uploads` (PUT), `complete?wait`, `POST /v1/assets` (серверне завантаження).
- Черга на SQLite (lease, ретраї, DLQ), пул `image`.
- `engine/image`: vips CLI, бюджетне кодування, AVIF+WebP, ThumbHash, dominant color.
- Дедуплікація sha256, refcount-GC, sweeper прострочених upload-ів.
- Outbox + вебхуки (Standard Webhooks).
- Квоти проєкту й namespace з резервуванням.
- `@kadr/node` (тікети, signUrl, verify webhook).
- **Інтеграція з Omi за feature flag** (staging).

**Готово, коли:** фото з iPhone (HEIC 4 МБ) → ≤ 20 КБ AVIF у R2 за p95 < 1.5 с від complete; golden-тести зелені.

## M2 — Video (2 тижні)

- S3 Multipart (тікети частин, complete з ETag), abort.
- `engine/video`: ffprobe-валідація, libx264 / VAAPI автодетект, постер, прогрес.
- Пул `video` з heartbeat lease; таймаути від тривалості.
- Квота хвилин відео на місяць.
- Сторінка Jobs в адмінці (live-прогрес через SSE, Retry/Cancel).

**Готово, коли:** 60-секундне відео 4K 300 МБ завантажується повз тунель, стискається до ≤ 5 МБ 720p і програється одразу (faststart).

## M3 — Private delivery & SDK (1–2 тижні)

- Worker `kadr-edge`: HMAC-перевірка, Cache API, Range-запити для відео, `Content-Disposition`.
- Signing keys: створення, ротація, публікація в Worker через Cloudflare API.
- Зміна видимості (CopyObject між бакетами).
- `@kadr/web` (pre-resize у Web Worker, multipart з паралельністю, ретраї частин, прогрес), `@kadr/react` (`<KadrImage>`, `<KadrVideo>`).
- OpenAPI 3.1 опубліковано; SDK генерується.
- **Підключення ofi та mif.**

## M4 — Product polish (2–3 тижні)

- Асет-експлорер (фільтри, перегляд метаданих, видалення).
- **Playground:** завантажити зразок, крутити параметри пресету, порівняння До/Після (split view), розмір/SSIM у реальному часі, «Зберегти як нову версію».
- Аналітика: прийнято / збережено / економія %, хвилини відео, failed-rate, p95 — по проєктах і днях.
- Журнал вебхуків з повторною відправкою; аудит-лог.
- URL-імпорт (SSRF-safe) і хмарні підключення (Google Drive / OneDrive / Dropbox через rclone).
- Litestream + `restore-drill`; `/metrics`, вбудовані алерти (Telegram/email).
- WebAuthn для адмінки.

## M5 — Scale & SaaS-ready (за потребою)

- `kadr worker` на окремих вузлах (internal lease API, mTLS); GPU-вузол з NVENC/QSV.
- HLS-драбина, спрайти таймлайну, AV1-рендиції.
- Рендиції на вимогу (lazy, кеш у R2) — або imgproxy за Worker-ом.
- Postgres-драйвер для `Store` (multi-instance control plane).
- Білінг: метеринг із `usage_daily` → Stripe; тарифні плани.
- Модерація контенту (NSFW-класифікатор як окремий пул), перевірка документів.
- Self-hosted пакет: `.deb`, інсталятор для Proxmox (helper-script), документація для клієнтів.

## Ризики

| Ризик | Вплив | Мітигація |
|---|---|---|
| Обмеження SQLite при рості записів | Затримки API | Один writer + батчинг; Postgres-драйвер у M5; поріг ≈ сотні записів/с — далеко від потреб S1 |
| Канал Proxmox на скачування відео з R2 | Черга відео росте | VAAPI, S2-воркери у VPS/Hetzner поруч із R2 |
| Зміна умов R2 (ціни, ToS) | Вартість | Абстракція `StorageDriver`; B2/Hetzner/Garage як заміна без зміни API |
| Якість ≤ 20 КБ на складних фото | Невдоволення користувачів | `photo-balanced` як альтернатива, `over_budget`-прапорець, Playground для підбору |
| Bus factor = 1 | Підтримка | ADR, OpenAPI, тести, runbook-и в OPERATIONS.md |
