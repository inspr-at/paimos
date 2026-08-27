#!/usr/bin/env bash
# PAI-297 — boot the dev stack, run the Playwright E2E smoke suite, tear down.
# Works on a dev box and in CI: the dev-login token is a throwaway value gated
# behind the `dev_login` build tag, never a prod credential. Pass extra args
# straight through to `playwright test` (e.g. `scripts/e2e.sh --headed`).
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Must be >= 32 chars (backend rejects shorter dev-login tokens at startup).
E2E_TOKEN="${PAIMOS_DEV_LOGIN_TOKEN:-e2e-smoke-token-not-for-prod-do-not-reuse}"
TOKDIR=$(mktemp -d)
printf 'PAIMOS_DEV_LOGIN_TOKEN=%s\n' "$E2E_TOKEN" >"$TOKDIR/token.env"
export PAIMOS_DEV_LOGIN_TOKEN_FILE="$TOKDIR/token.env"

# Role-smoke tests use the richer debug-* fixture accounts because they carry
# explicit editor/viewer/none and portal grants. Generate throwaway local
# passwords only so dev-seed can create those accounts; tests still authenticate
# through dev-login and never print or persist these values.
debug_password() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    printf 'e2e-debug-password-local-only-not-secret-%s-%s-%s-%s\n' "$RANDOM" "$RANDOM" "$RANDOM" "$RANDOM"
  fi
}

ensure_debug_password() {
  local name=$1
  if [[ -z "${!name:-}" ]]; then
    export "$name=$(debug_password)"
  fi
}

export PAIMOS_DEBUG_ACCOUNTS=1
ensure_debug_password PAIMOS_DEBUG_SUPERADMIN_PASSWORD
ensure_debug_password PAIMOS_DEBUG_ADMIN_PASSWORD
ensure_debug_password PAIMOS_DEBUG_USER_PASSWORD
ensure_debug_password PAIMOS_DEBUG_CUSTOMER_PASSWORD

# Visual snapshots must never inherit a developer's persistent devenv database:
# its accumulated issues and edited profiles make otherwise-correct pixels
# depend on local history. Always give that opt-in gate an isolated fixture DB.
# The broader smoke runner keeps its established explicit-DATA_DIR behavior.
if [[ "${PAIMOS_VISUAL_BASELINE:-}" == 1 ]]; then
  DATA_DIR=$(mktemp -d)
  export DATA_DIR
  CLEAN_DATA_DIR=1
elif [[ -z "${DATA_DIR:-}" ]]; then
  # The backend defaults DATA_DIR to /app/data (the container path), which isn't
  # writable on a CI runner with no direnv to point it elsewhere. Give it an
  # isolated, writable, throwaway dir so dev-seed can create the DB.
  DATA_DIR=$(mktemp -d)
  export DATA_DIR
  CLEAN_DATA_DIR=1
fi

# Agent Mode's geometry suite is an exact-build gate. Build the same `dist`
# artifact shipped by the Docker image and let that spec self-host it through
# Playwright routing; the remaining smoke specs still exercise the real Vite
# and Go stack below.
echo "→ building exact frontend artifact for visual gates"
(cd "$ROOT/frontend" && npm run build)
export PAI805_SELF_HOST_DIST=1

# Boot the stack (backend :8888 + vite :5173) in the background.
echo "→ booting dev stack (log: $ROOT/.e2e-devup.log)"
bash "$ROOT/scripts/dev-up.sh" >"$ROOT/.e2e-devup.log" 2>&1 &
DEVUP_PID=$!

cleanup() {
  echo "→ tearing down dev stack"
  pkill -P "$DEVUP_PID" 2>/dev/null || true
  kill "$DEVUP_PID" 2>/dev/null || true
  pkill -f 'dev-up-backend' 2>/dev/null || true
  pkill -f 'vite' 2>/dev/null || true
  rm -rf "$TOKDIR"
  if [[ "${CLEAN_DATA_DIR:-}" == 1 ]]; then
    rm -rf "$DATA_DIR"
  fi
}
trap cleanup EXIT

wait_for() {
  local url=$1 name=$2
  for _ in $(seq 1 60); do
    curl -sS -m 1 "$url" >/dev/null 2>&1 && {
      echo "  ✓ $name up"
      return 0
    }
    if ! kill -0 "$DEVUP_PID" 2>/dev/null; then
      echo "error: dev stack exited early — see $ROOT/.e2e-devup.log" >&2
      tail -20 "$ROOT/.e2e-devup.log" >&2 || true
      return 1
    fi
    sleep 2
  done
  echo "error: $name did not come up at $url" >&2
  return 1
}

echo "→ waiting for the stack"
wait_for http://localhost:8888/api/health backend
wait_for http://localhost:5173 vite

echo "→ running Playwright smoke"
cd "$ROOT/frontend"
PAIMOS_DEV_LOGIN_TOKEN="$E2E_TOKEN" E2E_API_URL="http://localhost:8888" \
  npx playwright test "$@"
