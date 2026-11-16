#!/usr/bin/env bash
# Runs on the Kadr host (as root, via `make deploy` / `make rollback`):
#   remote-release.sh deploy   <version> [root]   install /tmp/kadr-<version>, migrate, switch, restart
#   remote-release.sh rollback -         [root]   switch back to the previous release, restart
#
# Layout (docs/OPERATIONS.md §3):
#   <root>/releases/<version>/kadr
#   <root>/current  -> releases/<version>
#   <root>/previous    (file with the path of the release that was current before)
set -euo pipefail

action="${1:?action required (deploy|rollback)}"
version="${2:-}"
root="${3:-/opt/kadr}"
env_file="/etc/kadr/env"
ready_url="${KADR_READY_URL:-http://127.0.0.1:8081/readyz}"

log() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

run_as_kadr() {
  # Load the service environment exactly as systemd does.
  sudo -u kadr env -i PATH=/usr/local/bin:/usr/bin:/bin bash -c 'set -a; . "$0"; set +a; shift; exec "$@"' "$env_file" _ "$@"
}

wait_ready() {
  log "waiting for $ready_url"
  for _ in $(seq 1 60); do
    if curl -fsS --max-time 2 "$ready_url" >/dev/null 2>&1; then
      log "ready"
      return 0
    fi
    sleep 1
  done
  journalctl -u kadr -n 50 --no-pager || true
  die "kadr did not become ready within 60s"
}

switch_to() {
  local target="$1"
  local current
  current="$(readlink -f "$root/current" 2>/dev/null || true)"
  if [[ -n "$current" && "$current" != "$target" ]]; then
    echo "$current" > "$root/previous"
  fi
  ln -sfn "$target" "$root/current.new"
  mv -Tf "$root/current.new" "$root/current"
  log "current -> $target"
}

case "$action" in
  deploy)
    [[ -n "$version" ]] || die "version required"
    src="/tmp/kadr-$version"
    [[ -f "$src" ]] || die "$src not found"
    dest="$root/releases/$version"
    install -d -m 0755 "$dest"
    install -m 0755 "$src" "$dest/kadr"
    rm -f "$src"
    log "installed $dest/kadr ($("$dest/kadr" version))"

    log "running migrations"
    run_as_kadr "$dest/kadr" migrate up

    switch_to "$dest"
    systemctl restart kadr
    wait_ready
    ;;
  rollback)
    [[ -f "$root/previous" ]] || die "no previous release recorded in $root/previous"
    prev="$(cat "$root/previous")"
    [[ -x "$prev/kadr" ]] || die "previous release $prev is missing"
    switch_to "$prev"
    systemctl restart kadr
    wait_ready
    ;;
  *)
    die "unknown action $action"
    ;;
esac
