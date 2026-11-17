#!/usr/bin/env bash
# Idempotent provisioning of a Debian 13 (trixie) LXC container for Kadr.
# See docs/OPERATIONS.md §1–5 and docs/SECURITY.md §2, §4.
#
# Run as root inside the container from a checkout (or copy) of the repo:
#   sudo ./deploy/lxc/provision.sh
#
# Optional environment:
#   KADR_WITH_VAAPI=1          install the Intel VAAPI driver and vainfo
#   LITESTREAM_VERSION=0.3.13  install Litestream from GitHub releases ...
#   LITESTREAM_SHA256=<hex>    ... only together with its SHA-256 checksum
#
# Safe to re-run: existing keys, env files and configs are never overwritten.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KADR_USER=kadr
DATA_DIR=/var/lib/kadr
ETC_DIR=/etc/kadr
OPT_DIR=/opt/kadr

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
if ! grep -q 'VERSION_CODENAME=trixie' /etc/os-release 2>/dev/null; then
  warn "this script targets Debian 13 (trixie); continuing anyway"
fi

# ───────────── packages ─────────────
log "installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
packages=(libvips-tools ffmpeg rclone ghostscript qpdf ca-certificates tzdata curl sqlite3)
if [[ "${KADR_WITH_VAAPI:-0}" == "1" ]]; then
  packages+=(intel-media-va-driver vainfo)
fi
apt-get install -y -qq --no-install-recommends "${packages[@]}"

# ───────────── Litestream (optional, pinned + verified) ─────────────
if [[ -n "${LITESTREAM_VERSION:-}" ]]; then
  if command -v litestream >/dev/null && litestream version 2>/dev/null | grep -q "${LITESTREAM_VERSION}"; then
    log "litestream ${LITESTREAM_VERSION} already installed"
  elif [[ -z "${LITESTREAM_SHA256:-}" ]]; then
    warn "LITESTREAM_VERSION set without LITESTREAM_SHA256; refusing to install an unverified package"
  else
    log "installing litestream ${LITESTREAM_VERSION}"
    deb="$(mktemp --suffix=.deb)"
    curl -fsSL -o "$deb" "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-v${LITESTREAM_VERSION}-linux-amd64.deb"
    echo "${LITESTREAM_SHA256}  ${deb}" | sha256sum -c -
    apt-get install -y -qq "$deb"
    rm -f "$deb"
  fi
else
  command -v litestream >/dev/null || warn "litestream not installed (set LITESTREAM_VERSION and LITESTREAM_SHA256 to install it)"
fi

# ───────────── user and groups ─────────────
if ! id -u "$KADR_USER" >/dev/null 2>&1; then
  log "creating system user $KADR_USER"
  useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$KADR_USER"
fi
if getent group render >/dev/null; then
  usermod -aG render "$KADR_USER"
else
  warn "group 'render' does not exist (no iGPU passthrough); VAAPI will be unavailable"
fi

# ───────────── directories ─────────────
log "creating directories"
install -d -o "$KADR_USER" -g "$KADR_USER" -m 0750 "$DATA_DIR" "$DATA_DIR/tmp" "$DATA_DIR/db"
install -d -o root -g "$KADR_USER" -m 0750 "$ETC_DIR"
install -d -o root -g root -m 0755 "$OPT_DIR" "$OPT_DIR/releases"

# ───────────── secrets ─────────────
if [[ ! -f "$ETC_DIR/master.key" ]]; then
  log "generating master key $ETC_DIR/master.key (store a copy in your password manager!)"
  (umask 077; head -c 32 /dev/urandom | base64 > "$ETC_DIR/master.key")
fi
chown root:"$KADR_USER" "$ETC_DIR/master.key"
chmod 0440 "$ETC_DIR/master.key"

if [[ ! -f "$ETC_DIR/env" ]]; then
  log "writing $ETC_DIR/env (edit KADR_PUBLIC_ADDR before starting)"
  pepper="$(head -c 32 /dev/urandom | base64)"
  (umask 027; cat > "$ETC_DIR/env" <<EOF
# Kadr configuration (docs/OPERATIONS.md §4)
# /v1 API and /metrics: bind to the LAN interface only, e.g. <lan-ip>:8080
KADR_PUBLIC_ADDR=127.0.0.1:8080
# Admin UI: loopback only, exposed through cloudflared + Cloudflare Access
KADR_ADMIN_ADDR=127.0.0.1:8081
KADR_DB_PATH=$DATA_DIR/db/kadr.db
KADR_TMP_DIR=$DATA_DIR/tmp
KADR_MASTER_KEY_FILE=$ETC_DIR/master.key
KADR_API_KEY_PEPPER=$pepper
KADR_POOL_IMAGE=2
KADR_POOL_VIDEO=1
KADR_VIDEO_HW=auto
KADR_LOG_LEVEL=info
EOF
  )
fi
chown root:"$KADR_USER" "$ETC_DIR/env"
chmod 0640 "$ETC_DIR/env"

if [[ ! -f "$ETC_DIR/litestream.yml" ]]; then
  install -o root -g "$KADR_USER" -m 0640 "$REPO_DIR/deploy/litestream.yml" "$ETC_DIR/litestream.yml"
fi
if [[ ! -f "$ETC_DIR/litestream.env" ]]; then
  (umask 027; cat > "$ETC_DIR/litestream.env" <<'EOF'
# R2 token with access to the system bucket only (kadr-backup)
CF_ACCOUNT_ID=
LITESTREAM_ACCESS_KEY_ID=
LITESTREAM_SECRET_ACCESS_KEY=
EOF
  )
  chown root:"$KADR_USER" "$ETC_DIR/litestream.env"
  chmod 0640 "$ETC_DIR/litestream.env"
fi

# ───────────── systemd ─────────────
log "installing systemd units"
install -m 0644 "$REPO_DIR/deploy/systemd/kadr.service" /etc/systemd/system/kadr.service
install -m 0644 "$REPO_DIR/deploy/systemd/litestream.service" /etc/systemd/system/litestream.service
systemctl daemon-reload

cat <<EOF

Provisioning complete. Next steps:
  1. Edit $ETC_DIR/env (KADR_PUBLIC_ADDR) and $ETC_DIR/litestream.env.
  2. From your workstation: make release VERSION=x.y.z && make deploy HOST=<this-host> VERSION=x.y.z
     (the first deploy also runs 'kadr migrate up').
  3. Create the first administrator:
       sudo -u $KADR_USER bash -c 'set -a; . $ETC_DIR/env; set +a; $OPT_DIR/current/kadr admin create --email you@example.com'
  4. Verify: sudo -u $KADR_USER bash -c 'set -a; . $ETC_DIR/env; set +a; $OPT_DIR/current/kadr doctor'
  5. Enable services: systemctl enable --now litestream kadr
EOF
