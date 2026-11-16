# ADR-0002: Один unprivileged LXC, systemd, без Docker

**Статус:** Accepted

## Контекст
Інфраструктура — Proxmox. Docker усередині LXC додає dockerd/containerd (~200–350 МБ RAM), ускладнює прокидання `/dev/dri` і дублює ізоляцію, яку вже дає LXC.

## Рішення
Debian 13 unprivileged LXC; процеси `kadr` і `litestream` під systemd з пісочницею (див. SECURITY.md §4). Обробники (`vips`, `ffmpeg`, `rclone`) — системні пакети, що запускаються під задачу.

## Наслідки
- Мінімальний простій, прямий доступ до iGPU.
- Оновлення кодеків через `apt` (security-патчі Debian).
- Відтворюваність середовища забезпечує `kadr doctor` + скрипт провізії (`deploy/lxc/provision.sh`), а не образ.
- Для зовнішніх self-hosted клієнтів у майбутньому можна додатково випустити `.deb` і, за потреби, Docker-образ — архітектура від цього не залежить.

## Альтернативи
- Docker Compose у VM/LXC — зайва RAM, складніший GPU passthrough.
- Два LXC (control plane + worker) — передчасно; шлях закладено через `kadr worker` (ADR-0003, ARCHITECTURE §8).
