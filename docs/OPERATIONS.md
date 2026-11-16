# Експлуатація

## 1. Proxmox LXC

| Параметр | Значення |
|---|---|
| Шаблон | Debian 13 (trixie), **unprivileged** |
| CPU | 2 vCPU (старт) → 4 vCPU (регулярне відео без iGPU) |
| RAM | 2048 МБ + swap 512 МБ |
| Диск | 30 ГБ на NVMe-сторі (ОС ~2 ГБ, решта — tmp для відео) |
| Мережа | Статичний IP у 192.168.50.0/24, DNS-імʼя `kadr.lan` |
| Features | `nesting=0`, `keyctl=0` |
| Автостарт | `onboot: 1`, `startup: order=3` (після мережі/БД-хостів) |

### Прокидання Intel iGPU (VAAPI), опційно

`/etc/pve/lxc/<ID>.conf`:

```ini
dev0: /dev/dri/renderD128,gid=993,mode=0660
```

(Proxmox 8.2+; для старіших версій — `lxc.cgroup2.devices.allow: c 226:128 rwm` + `lxc.mount.entry`.) Усередині LXC: користувач `kadr` у групі `render`, пакет `intel-media-va-driver`, перевірка — `vainfo`.

## 2. Пакети

```bash
apt update && apt install -y --no-install-recommends \
  libvips-tools ffmpeg rclone ca-certificates tzdata \
  intel-media-va-driver vainfo   # тільки якщо є iGPU
# Litestream — офіційний .deb з GitHub releases (фіксована версія + перевірка SHA-256)
useradd --system --home /var/lib/kadr --shell /usr/sbin/nologin kadr
install -d -o kadr -g kadr -m 0750 /var/lib/kadr /var/lib/kadr/tmp /var/lib/kadr/db
install -d -o root -g kadr -m 0750 /etc/kadr
```

Перевірка AVIF у vips: `vips heifsave` має підтримувати `compression=av1` (libheif + libaom/svt). Команда `kadr doctor` перевіряє все це автоматично.

## 3. Розкладка файлів

```
/opt/kadr/releases/<version>/kadr   # бінарник із вбудованою адмінкою
/opt/kadr/current -> releases/<version>
/etc/kadr/env                       # конфіг (0640 root:kadr)
/etc/kadr/master.key                # KEK (0440 root:kadr)
/etc/kadr/litestream.yml
/var/lib/kadr/db/kadr.db            # + -wal, -shm
/var/lib/kadr/tmp/<job_id>/         # робочі каталоги задач
```

## 4. Конфіг `/etc/kadr/env`

```bash
KADR_PUBLIC_ADDR=192.168.50.130:8080     # /v1, тільки LAN-інтерфейс
KADR_ADMIN_ADDR=127.0.0.1:8081           # адмінка, віддається через cloudflared
KADR_DB_PATH=/var/lib/kadr/db/kadr.db
KADR_TMP_DIR=/var/lib/kadr/tmp
KADR_MASTER_KEY_FILE=/etc/kadr/master.key
KADR_API_KEY_PEPPER=…
KADR_POOL_IMAGE=2
KADR_POOL_VIDEO=1
KADR_VIDEO_HW=auto                       # auto | vaapi | off
KADR_CF_ACCOUNT_ID=…                     # для публікації ключів у kadr-edge
KADR_CF_API_TOKEN=…                      # scope: Workers Scripts:Edit (лише цей Worker)
KADR_LOG_LEVEL=info
```

Креденшели R2 зберігаються не тут, а зашифрованими в БД (`storage_profiles`) — їх вводять в адмінці.

## 5. systemd

`deploy/systemd/kadr.service`, `deploy/systemd/litestream.service`. Перший запуск:

```bash
kadr migrate up
kadr admin create --email you@domain
systemctl enable --now litestream kadr
```

## 6. Деплой із Mac

```bash
make release VERSION=0.3.0      # web build → go build (CGO_ENABLED=0 GOOS=linux GOARCH=amd64 -trimpath -ldflags "-s -w -X main.version=…")
make deploy  HOST=kadr.lan      # scp → /opt/kadr/releases/0.3.0 → kadr migrate up → ln -sfn → systemctl restart → /readyz
make rollback HOST=kadr.lan     # ln -sfn на попередню версію → restart
```

**Graceful restart:** при SIGTERM Kadr припиняє брати задачі, чекає завершення фото (≤ 30 с), повертає відео-задачі в чергу (`lease` скидається), закриває HTTP. `TimeoutStopSec=60`.

Міграції лише вперед і сумісні з попередньою версією (expand → migrate → contract), щоб rollback бінарника був безпечним.

## 7. Cloudflare R2: налаштування

1. Бакети `kadr-public`, `kadr-private`, `kadr-system`, location hint **EEUR**.
2. `kadr-public` → Custom Domain `cdn.<domain>`; Cache Rule: Cache Everything, Edge TTL 1 рік.
3. `kadr-private` → Public access **OFF**; CORS:
   ```json
   [{ "AllowedOrigins": ["https://omi.kim","https://<ofi>","https://<mif>","http://localhost:3000"],
      "AllowedMethods": ["PUT","HEAD"], "AllowedHeaders": ["content-type","content-length","x-amz-*"],
      "ExposeHeaders": ["ETag"], "MaxAgeSeconds": 3600 }]
   ```
   Lifecycle: `tmp/` delete after 1 day; abort incomplete multipart after 1 day.
4. `kadr-system` → lifecycle: delete noncurrent після 30 днів.
5. API-токени: `kadr-rw` (Object R/W на public + private), `kadr-backup` (Object R/W тільки system).
6. Worker `kadr-edge` на `media.<domain>` з R2 binding `PRIVATE` (read) і секретами ключів підпису.
7. Billing alert на $5.

## 8. Бекапи й відновлення

| Що | Як | RPO / RTO |
|---|---|---|
| `kadr.db` | Litestream → `kadr-system` (sync 1 с, snapshot щодоби, ретеншн 30 днів) | RPO ≈ 1 с / RTO ≈ 5 хв |
| Медіа | Живе в R2 (11 девʼяток надійності). Для критичних проєктів — нічна реплікація `kadr-public`/`kadr-private` → Backblaze B2 через `rclone sync` | RPO 24 год |
| Майстер-ключ | Менеджер паролів + офлайн-копія | — |
| LXC | Proxmox vzdump щотижня (не обовʼязково: все відновлюється з бінарника + Litestream) | — |

**Відновлення з нуля:** новий LXC → пакети → бінарник → `/etc/kadr/*` → `litestream restore -o /var/lib/kadr/db/kadr.db s3://kadr-system/…` → `systemctl start kadr`. Щомісяця — тестове відновлення у staging (`make restore-drill`).

## 9. Моніторинг

- `/healthz` (живий), `/readyz` (БД + R2 + vips/ffmpeg доступні).
- `/metrics` (Prometheus-формат, тільки LAN): `kadr_jobs_queued{pool}`, `kadr_job_duration_seconds{kind}`, `kadr_job_failures_total{kind,code}`, `kadr_bytes_in_total`, `kadr_bytes_stored_total`, `kadr_r2_requests_total{op,status}`, `kadr_webhook_deliveries_total{status}`, `kadr_tmp_disk_free_bytes`, `kadr_db_wal_bytes`.
- Алерти (через Grafana або вбудований алертинг адмінки → Telegram/email):
  - черга відео > 10 задач або найстаріша > 15 хв;
  - частка failed > 5% за 15 хв;
  - вільне місце tmp < 5 ГБ;
  - Litestream lag > 60 с;
  - dead-вебхуки > 0.
- Логи: JSON у journald (`journalctl -u kadr -o cat | jq`), `request_id`/`job_id`/`asset_id` у кожному записі.

## 10. Розрахунок ємності

| Навантаження/міс | Фото (photo-lite) | Відео 30 с (720p) | CPU-годин | R2 зберігання (накопичувально) |
|---|---|---|---|---|
| Старт | 50 000 | 500 | ~3 | ~1 ГБ + ~1 ГБ |
| Ріст | 500 000 | 5 000 | ~25 | ~10 ГБ + ~10 ГБ |
| Масштаб | 5 000 000 | 50 000 | ~250 → потрібні S2-воркери / VAAPI | ~100 ГБ + ~100 ГБ → ~$3/міс |

Канал Proxmox: кожне відео тягне сирець із R2 (50–150 МБ) і віддає ~2–3 МБ; 5 000 відео/міс ≈ 500 ГБ вхідного трафіку на домашній/VPS канал — це потрібно врахувати в тарифі провайдера.
