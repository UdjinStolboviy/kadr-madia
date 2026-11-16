# API Kadr v1

Джерело правди — `api/openapi.yaml` (OpenAPI 3.1). З нього генеруються `@kadr/node`, `@kadr/web` і клієнт адмінки. Цей документ — огляд.

## Загальне

| | |
|---|---|
| Base URL | `http://kadr.lan:8080/v1` (тільки LAN) |
| Автентифікація | `Authorization: Bearer kd_live_…` або `kd_test_…` |
| Формат | JSON, `snake_case` у полях |
| Ідемпотентність | `Idempotency-Key` для всіх POST (зберігається 24 год) |
| Помилки | RFC 9457 `application/problem+json` з полем `code` |
| Пагінація | курсорна: `?limit=50&cursor=…` → `next_cursor` |
| ID | ULID із префіксом: `ast_…`, `upl_…`, `blb_…`, `prj_…` |
| Версіонування | у шляху (`/v1`); додавання полів — не breaking change |

### Scopes ключів

`uploads:write` · `assets:read` · `assets:write` · `assets:delete` · `urls:sign` · `namespaces:admin` · `imports:write`

## Завантаження

### `POST /v1/uploads` — створити тікет

```json
{
  "preset": "photo-lite",
  "namespace": "org_8f2c",
  "visibility": "private",
  "filename": "receipt.jpg",
  "content_type": "image/jpeg",
  "size": 384211,
  "sha256": "9f86d0…",
  "metadata": { "entity": "receipt", "entity_id": "rcp_123" }
}
```

**201** — потрібне завантаження:

```json
{
  "upload_id": "upl_01JD…",
  "method": "PUT",
  "url": "https://<acc>.r2.cloudflarestorage.com/kadr-private/tmp/omi/upl_01JD…?X-Amz-…",
  "headers": { "Content-Type": "image/jpeg", "Content-Length": "384211" },
  "expires_at": "2026-11-16T21:15:00Z"
}
```

Для `size` > 64 МБ: `"method": "MULTIPART"`, `"part_size": 16777216`, `"parts": [{ "number": 1, "url": "…" }, …]` (перші 20 частин; решту дає `POST /v1/uploads/{id}/parts`).

**200** — дедуплікація (`sha256` уже відомий у межах `project + visibility + preset_version`): одразу повертає готовий `asset`, завантаження не потрібне.

**Помилки:** `quota_exceeded` (402), `unsupported_media` (415), `too_large` (413), `preset_not_found` (404).

### `POST /v1/uploads/{id}/parts` — отримати ще URL частин

`{ "part_numbers": [21, 22, 23] }` → `{ "parts": [...] }`

### `POST /v1/uploads/{id}/complete?wait=5s`

Тіло для multipart: `{ "parts": [{ "number": 1, "etag": "…" }, …] }`.

- Повертає `asset`. Якщо обробка завершилась у межах `wait` (типово для фото) — `status: "ready"`, інакше `status: "processing"` і пізніше вебхук `asset.ready`.
- Ідемпотентний.

### `DELETE /v1/uploads/{id}` — скасувати

Скасовує multipart, видаляє tmp-обʼєкт і звільняє резерв квоти.

### `POST /v1/assets` — серверне завантаження (multipart/form-data)

Для бекенд-процесів (сканер чеків, імпорти): файл ≤ 25 МБ іде в Kadr по LAN напряму, без R2 tmp.

### `POST /v1/imports/url`

`{ "url": "https://…", "preset": "photo-lite", "namespace": "org_8f2c", "visibility": "public" }` → `202 { asset (processing) }`

### `POST /v1/imports/cloud`

`{ "connection_id": "con_…", "file_id": "…", "preset": "…", "namespace": "…", "visibility": "…" }` → `202`

## Асети

### Обʼєкт `asset`

```json
{
  "id": "ast_01JD…",
  "status": "ready",
  "kind": "image",
  "namespace": "org_8f2c",
  "visibility": "public",
  "preset": "photo-lite",
  "preset_version": 3,
  "original": { "filename": "IMG_2231.HEIC", "content_type": "image/heic", "bytes": 4382110, "width": 4032, "height": 3024 },
  "renditions": [
    { "name": "main", "format": "avif", "bytes": 19422, "width": 800, "height": 600, "url": "https://cdn.domain/omi/blb_01JD…/main.avif" },
    { "name": "main", "format": "webp", "bytes": 20110, "width": 800, "height": 600, "url": "https://cdn.domain/omi/blb_01JD…/main.webp" }
  ],
  "placeholder": { "thumbhash": "1QcSHQRnh493V4dIh4eXh1h4kJUI", "dominant_color": "#a3866c" },
  "duration_sec": null,
  "poster": null,
  "metadata": { "entity": "listing", "entity_id": "lst_77" },
  "savings_ratio": 0.991,
  "created_at": "2026-11-16T20:59:12Z",
  "ready_at": "2026-11-16T20:59:13Z"
}
```

Для `visibility: "private"` поле `url` відсутнє, натомість `key` — його підписують через SDK або `POST /v1/signed-urls`.

| Метод | Шлях | Опис |
|---|---|---|
| GET | `/v1/assets/{id}` | Отримати |
| GET | `/v1/assets?namespace=&kind=&status=&cursor=` | Список |
| PATCH | `/v1/assets/{id}` | `metadata`, `visibility` (запускає копіювання між бакетами) |
| DELETE | `/v1/assets/{id}` | Видалити (обʼєкти в R2 видаляє GC за refcount) |
| POST | `/v1/assets/{id}/reprocess` | Переобробити з іншою/новою версією пресету |

## Підписані URL (приватні файли)

### Локально в SDK (рекомендовано, без мережі)

```ts
const url = kadr.signUrl(asset.renditions[0].key, { ttl: 3600, download: "receipt.avif" });
// https://media.domain/omi/blb_…/main.avif?exp=1763330400&kid=k3&sig=Base64Url(HMAC-SHA256(secret, "GET\n/omi/blb_…/main.avif\n1763330400\nreceipt.avif"))
```

### Через API

`POST /v1/signed-urls` `{ "items": [{ "asset_id": "ast_…", "rendition": "main", "ttl": 3600 }] }` → `{ "items": [{ "url": "…", "expires_at": "…" }] }`

## Простори імен і квоти

| Метод | Шлях | Опис |
|---|---|---|
| GET | `/v1/namespaces/{ns}/usage` | Байти, кількість, хвилини відео за місяць, ліміти |
| PUT | `/v1/namespaces/{ns}/quota` | Встановити ліміти (тариф організації в проєкті) |
| DELETE | `/v1/namespaces/{ns}` | Повне видалення даних простору (GDPR), асинхронно, подія `namespace.purged` |

## Пресети

`GET /v1/presets` · `GET /v1/presets/{slug}` (керування — через адмінку або `PUT /v1/presets/{slug}` зі scope `namespaces:admin`).

## Вебхуки

Формат — [Standard Webhooks](https://www.standardwebhooks.com/): заголовки `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256>`.

```json
{ "type": "asset.ready", "timestamp": "2026-11-16T20:59:13Z", "data": { "asset": { … }, "metadata": { … } } }
```

| Подія | Коли |
|---|---|
| `asset.ready` | Обробку завершено |
| `asset.failed` | Помилка (`error.code`: `unsupported_media`, `too_long`, `corrupt`, `timeout`, …) |
| `asset.updated` | Змінено видимість або метадані |
| `asset.deleted` | Видалено |
| `upload.expired` | Тікет прострочено без файлу |
| `quota.warning` | Простір імен використав ≥ 80% |
| `quota.exceeded` | Ліміт вичерпано |
| `namespace.purged` | Видалення простору завершено |

Ретраї: 10 с, 30 с, 2 хв, 10 хв, 30 хв, 1 год, 3 год, 6 год, 12 год (разом ~24 год), потім `dead` + повторна відправка з адмінки. Отримувач має бути ідемпотентним за `webhook-id`.

## Admin API

`/admin/api/*` на окремому порту, сесія в cookie (`HttpOnly`, `Secure`, `SameSite=Strict`) + CSRF-токен. Ресурси: `projects`, `api-keys`, `signing-keys`, `storage-profiles`, `presets`, `webhooks`, `deliveries`, `jobs`, `assets`, `connections`, `stats`, `audit`, `system`. Не є публічним контрактом.
