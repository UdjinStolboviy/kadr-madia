# Безпека

## 1. Модель загроз (STRIDE, коротко)

| Загроза | Вектор | Захист |
|---|---|---|
| **Spoofing** | Викрадений API-ключ проєкту | Ключі тільки в бекендах; LAN-only API; scopes; ротація; `last_used_at` + аудит; відкликання миттєве |
| | Підробка приватного URL | HMAC-SHA256, обовʼязковий `exp`, `kid`, порівняння в константний час |
| | Підробка вебхука | Standard Webhooks HMAC + timestamp (вікно 5 хв) |
| **Tampering** | Підміна файлу між тікетом і complete | Підписані `Content-Length` і `Content-Type` у presigned PUT; `HEAD` перед обробкою; перевірка `sha256` після завантаження |
| **Repudiation** | Хто змінив пресет / видалив ключ | `audit_log` для всіх адмін-дій і чутливих API-викликів |
| **Information disclosure** | Приватний файл стає публічним | Фізично окремі бакети; дедуп не перетинає visibility; публічний бакет не має прав запису з Worker |
| | Витік GPS/EXIF | Метадані завжди видаляються (vips strip, ffmpeg `-map_metadata -1`) |
| | Перебір публічних URL | ULID blob_id (80 біт ентропії), немає листингу бакета |
| | Витік секретів із БД | Envelope-шифрування; майстер-ключ поза БД і поза бекапом |
| **DoS** | «Бомби» (decompression, 100k×100k PNG, відео на 10 год) | `maxInputBytes`, `maxInputPixels`, ffprobe-ліміти до запуску ffmpeg, таймаути процесів, семафори пулів, квоти |
| | Масові тікети | Rate limit на ключ і namespace (token bucket), резерв квоти |
| **Elevation of privilege** | RCE через баг кодека | Дочірні процеси без shell (`exec.Command` з масивом аргументів), окремий непривілейований користувач `kadr`, systemd sandboxing, unprivileged LXC, оновлення libvips/ffmpeg |
| | SSRF через URL-імпорт | Резолв → перевірка IP (RFC1918, loopback, link-local, CGNAT, metadata, IPv6 ULA) → dial на перевірений IP; тільки http/https; ліміт редиректів із повторною перевіркою |

## 2. Ключі та секрети

| Секрет | Де живе | Формат |
|---|---|---|
| Майстер-ключ (KEK) | `/etc/kadr/master.key` (0400, owner kadr), копія в менеджері паролів | 32 байти |
| Pepper для API-ключів | `/etc/kadr/env` | 32 байти |
| API-ключі проєктів | Лише SHA-256(secret‖pepper) у БД | `kd_live_<8 символів префікса>_<32 байти base62>` |
| Секрети підпису URL | `signing_keys.secret_enc` + Worker secrets | 32 байти, `kid` |
| Креденшели S3/R2, OAuth-токени, секрети вебхуків | `*_enc` у БД | AES-256-GCM, DEK на запис, DEK зашифрований KEK |
| Сесії адмінки | SHA-256 токена в БД | 32 байти, cookie `__Host-kadr_session` |

**Ротація:**
- `kadr keys rotate-master` — перешифровує всі DEK новим KEK (дані не чіпаються).
- Ключ підпису URL: новий → `active`, старий → `verify_only` на max TTL (наприклад, 24 год) → `retired`. Worker отримує обидва.
- API-ключі: кілька активних одночасно для безшовної заміни.

## 3. Мережева ізоляція

```
Інтернет ──► Cloudflare ──► Tunnel ──► kadr:8081 (/admin) — лише через Cloudflare Access (email + MFA)
LAN 192.168.50.0/24 ──► kadr:8080 (/v1) — firewall LXC: allow тільки IP omi/ofi/mif-api
kadr ──► R2 / вебхуки / імпорт — вихідні зʼєднання
```

- `/v1` не опубліковано в тунелі взагалі.
- Адмінка: Cloudflare Access + власний логін (argon2id) + TOTP. Опційно WebAuthn (M4).
- Додатково: перевірка заголовка `Cf-Access-Jwt-Assertion` на сервері адмінки.

## 4. systemd-пісочниця

```ini
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=no            # потрібен /dev/dri для VAAPI; без iGPU → yes
DeviceAllow=/dev/dri/renderD128 rw
ReadWritePaths=/var/lib/kadr
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=no    # libx264/libaom використовують JIT-асемблер
SystemCallArchitectures=native
CapabilityBoundingSet=
```

## 5. Обробка вхідних файлів

1. Розмір перевіряється тричі: при тікеті (декларований), підписом R2 (`Content-Length`), `HEAD` перед завантаженням.
2. Тип визначається за magic bytes, а не за розширенням чи `Content-Type`.
3. Растрові: `vipsheader` → ширина × висота ≤ `maxInputPixels` до будь-якого декодування.
4. Відео: `ffprobe` з таймаутом 10 с → тривалість, кодеки, роздільність, кількість потоків.
5. Кожна задача — окремий каталог `/var/lib/kadr/tmp/<job_id>/`, видаляється в `defer`; sweeper прибирає залишки під час старту.
6. Таймаути процесів: фото 30 с, відео `duration × 4 + 60 с`.

## 6. Приватність і відповідність

- **GDPR / право на видалення:** `DELETE /v1/namespaces/{ns}` і `DELETE /v1/assets/{id}`; фізичне видалення з R2 через GC (SLA ≤ 24 год), підтвердження подією.
- **Мінімізація даних:** оригінали за замовчуванням не зберігаються; EXIF/GPS видаляються.
- **Регіон даних:** R2 location hint EEUR/WEUR або jurisdiction `eu` для EU-only.
- **Логи:** без вмісту файлів, без повних підписаних URL і секретів (редакція в slog).

## 7. Ланцюг постачання

- `go mod verify`, `govulncheck` у CI; Renovate для залежностей.
- Відтворювані збірки (`-trimpath`, фіксований Go), SHA-256 і підпис артефакту (cosign / minisign).
- `ffmpeg`, `libvips`, `rclone` — з офіційних пакетів Debian (security-оновлення через `unattended-upgrades`).
