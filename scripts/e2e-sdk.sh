#!/usr/bin/env bash
# Contract/e2e test of the Node SDK (sdk/node) against a real Kadr server.
#
# Builds kadr, starts it with KADR_DEV=1 in a temporary data directory with
# --dev-storage (in-process S3) and --fake-engine (no libvips needed),
# creates an administrator and runs `pnpm test:e2e` in sdk/node. The test
# itself creates the project, API key and webhook endpoint via the admin API.
#
# Usage: scripts/e2e-sdk.sh            (KEEP_E2E_DIR=1 keeps the temp dir)
# Requires: go, node >= 20, pnpm, curl.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="${TMPDIR:-/tmp}"
WORK="$(mktemp -d "${TMP_ROOT%/}/kadr-e2e.XXXXXX")"
PID=""

cleanup() {
  local status=$?
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null || true
    for _ in $(seq 1 100); do
      kill -0 "$PID" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if [[ $status -ne 0 && -f "$WORK/kadr.log" ]]; then
    echo "---- kadr.log (last 80 lines) ----" >&2
    tail -n 80 "$WORK/kadr.log" >&2 || true
  fi
  if [[ "${KEEP_E2E_DIR:-}" == "1" ]]; then
    echo "kept $WORK" >&2
  else
    rm -rf "$WORK"
  fi
  exit $status
}
trap cleanup EXIT INT TERM

free_port() {
  node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})'
}

wait_http() {
  local url=$1
  for _ in $(seq 1 150); do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then return 0; fi
    if ! kill -0 "$PID" 2>/dev/null; then echo "kadr exited during startup" >&2; return 1; fi
    sleep 0.1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

echo "==> building kadr"
(cd "$ROOT" && go build -o "$WORK/kadr" ./cmd/kadr)

PUBLIC_PORT="$(free_port)"
ADMIN_PORT="$(free_port)"
STORAGE_PORT="$(free_port)"
ADMIN_EMAIL="admin@example.com"
ADMIN_PASSWORD="e2e-$(node -e 'console.log(require("crypto").randomBytes(12).toString("hex"))')"

export KADR_DEV=1
export KADR_DB_PATH="$WORK/data/kadr.db"
export KADR_TMP_DIR="$WORK/tmp"
export KADR_MASTER_KEY_FILE="$WORK/master.key"
export KADR_PUBLIC_ADDR="127.0.0.1:$PUBLIC_PORT"
export KADR_ADMIN_ADDR="127.0.0.1:$ADMIN_PORT"
export KADR_DEV_STORAGE_ADDR="127.0.0.1:$STORAGE_PORT"
export KADR_LOG_LEVEL="${KADR_LOG_LEVEL:-info}"
export KADR_POOL_IMAGE=2

echo "==> preparing $WORK"
"$WORK/kadr" migrate up >/dev/null
printf '%s\n' "$ADMIN_PASSWORD" | "$WORK/kadr" admin create --email "$ADMIN_EMAIL" --password-stdin >/dev/null

echo "==> starting kadr (public :$PUBLIC_PORT, admin :$ADMIN_PORT, dev storage :$STORAGE_PORT)"
"$WORK/kadr" serve --dev-storage --fake-engine >"$WORK/kadr.log" 2>&1 &
PID=$!
wait_http "http://127.0.0.1:$PUBLIC_PORT/readyz"
wait_http "http://127.0.0.1:$ADMIN_PORT/readyz"

echo "==> running SDK e2e tests"
cd "$ROOT/sdk/node"
KADR_E2E_PUBLIC_URL="http://127.0.0.1:$PUBLIC_PORT" \
KADR_E2E_ADMIN_URL="http://127.0.0.1:$ADMIN_PORT" \
KADR_E2E_ADMIN_EMAIL="$ADMIN_EMAIL" \
KADR_E2E_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  pnpm test:e2e --reporter=verbose

echo "==> e2e OK"
