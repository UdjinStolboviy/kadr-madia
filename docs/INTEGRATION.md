# Інтеграція з omi / ofi / mif

## 1. Що отримує кожен проєкт

| | omi | ofi | mif |
|---|---|---|---|
| Проєкт у Kadr | `omi` (live), `omi-staging` (test) | `ofi`, `ofi-staging` | `mif`, `mif-staging` |
| Ключ | `kd_live_…` у `KADR_API_KEY` бекенду | — | — |
| Namespace | `householdId` / `orgId` | організація | організація |
| Вебхук | `http://<omi-api LAN>/internal/kadr/webhook` | … | … |
| Публічні URL | `https://cdn.<domain>/omi/…` | `…/ofi/…` | `…/mif/…` |
| Приватні URL | `https://media.<domain>/omi/…?exp&kid&sig` | … | … |

## 2. Бекенд (NestJS, `@kadr/node`)

```ts
// infrastructure/media/kadr.client.ts
import { Kadr } from "@kadr/node";

export const kadr = new Kadr({
  baseUrl: process.env.KADR_URL!,          // http://kadr.lan:8080
  apiKey: process.env.KADR_API_KEY!,
  signing: { kid: process.env.KADR_SIGNING_KID!, secret: process.env.KADR_SIGNING_SECRET!, baseUrl: "https://media.<domain>" },
});

// 1) тікет
const ticket = await kadr.uploads.create({
  preset: "photo-lite", namespace: orgId, visibility: "private",
  contentType, size, sha256, metadata: { entity: "receipt", entityId },
}, { idempotencyKey: `${userId}:${clientUploadId}` });

// 2) complete
const asset = await kadr.uploads.complete(ticket.uploadId, { parts, wait: "5s" });

// 3) приватне посилання — локально, без запиту в Kadr
const url = kadr.signUrl(asset.renditions[0].key, { ttl: 3600 });

// 4) вебхук
const event = kadr.webhooks.verify(rawBody, headers, process.env.KADR_WEBHOOK_SECRET!);
```

### Omi: точки змін

| Де | Що |
|---|---|
| `modules/media/domain/storage/storage-provider.interface.ts` | Новий тип `KADR` у `StorageProviderType`, реалізація `KadrStorageProvider` |
| `StorageProviderFactory` | Реєстрація `KADR` як платформного (`IntegrationSource.PLATFORM`) провайдера за замовчуванням |
| `StorageUploadMode` | Новий режим `DIRECT_PUT` (і `DIRECT_MULTIPART`) поряд із `DIRECT_POST` / `SERVER_PROXY` |
| Фронтенд `direct-upload.ts` | Гілка для `DIRECT_PUT`: `fetch(url, { method: "PUT", body, headers })` або `@kadr/web` |
| `FileMedia` | Поля `kadrAssetId`, `thumbhash`, `status` (processing/ready/failed) |
| `receipt-media.service`, сканери | Серверне завантаження `POST /v1/assets` по LAN |
| Каскадне видалення (`onDelete: Cascade`) | Outbox-подія → `kadr.assets.delete(id)`; нічна звірка в `VectorAndMediaPurgeService` |
| Новий ендпоінт | `POST /internal/kadr/webhook` (тільки LAN, перевірка підпису) |

Існуючі BYOS-провайдери Omi (Cloudinary/ImageKit/Uploadcare) залишаються — Kadr стає платформним варіантом за замовчуванням.

## 3. Фронтенд (`@kadr/web`, `@kadr/react`)

```tsx
import { uploadFile } from "@kadr/web";
import { KadrImage } from "@kadr/react";

const asset = await uploadFile(file, {
  createTicket: (meta) => api.post("/media/uploads", meta),          // через бекенд проєкту
  complete: (id, parts) => api.post(`/media/uploads/${id}/complete`, { parts }),
  preResize: { maxSide: 1600, quality: 0.9 },                         // Web Worker + OffscreenCanvas
  onProgress: (p) => setProgress(p),
});

<KadrImage asset={asset} sizes="(max-width: 640px) 100vw, 400px" />
// → <picture> з AVIF + WebP, ThumbHash-заглушка до завантаження, width/height без CLS
```

## 4. План міграції Omi

1. **Тінь:** Kadr розгорнуто, проєкт `omi-staging`, нові завантаження у staging ідуть через Kadr.
2. **Feature flag** `media.provider=kadr` для частини household-ів у production.
3. **Бекфіл:** скрипт проходить по `FileMedia` → `POST /v1/imports/url` (з існуючих URL) → оновлює посилання; старі файли видаляються після перевірки.
4. **Перемикання за замовчуванням**, моніторинг failed-rate і p95 обробки.
5. ofi і mif підключаються за тим самим шаблоном (≈ 1 день на проєкт).
