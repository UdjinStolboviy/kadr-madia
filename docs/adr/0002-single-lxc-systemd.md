# ADR-0002: Single unprivileged LXC/host, systemd, no Docker

**Status:** Accepted

## Context
The reference deployment is a Proxmox LXC; any small Linux host with systemd must work too. Docker inside an LXC adds dockerd/containerd (~200–350 MB RAM), complicates `/dev/dri` passthrough, and duplicates the isolation the container already provides.

## Decision
Debian 13 unprivileged LXC (or VM/bare metal); `kadr` and `litestream` run under systemd with sandboxing (see SECURITY.md §4). Processors (`vips`, `ffmpeg`, `rclone`) are system packages launched per job.

## Consequences
- Minimal idle footprint, direct iGPU access.
- Codec updates via `apt` (Debian security patches).
- Environment reproducibility comes from `kadr doctor` + the provisioning script (`deploy/lxc/provision.sh`), not from an image.
- For self-hosted customers we can additionally ship a `.deb` and, if requested, a Docker image — the architecture does not depend on either.

## Alternatives
- Docker Compose in a VM/LXC — extra RAM, harder GPU passthrough.
- Two containers (control plane + worker) — premature; the path is designed in via `kadr worker` (ADR-0003, ARCHITECTURE §8).
