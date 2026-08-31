#!/usr/bin/env bash
# PAI-695 — regenerate the complete paimos.inspr.at product-image set from
# the local seeded demo workspace, then publish it into the canonical site
# checkout with release provenance and hotspot-framing verification.

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck disable=SC1091
source "$ROOT/scripts/release-version.sh"
SITE_ROOT="${1:-$ROOT/../inspr-at}"
CAPTURE_RELEASE=$(tr -d '[:space:]' < "$ROOT/VERSION")
API_URL=http://localhost:8888
APP_URL=http://localhost:5173
CAPTURE_DIR=""

cleanup() {
  if [[ -n "$CAPTURE_DIR" && -d "$CAPTURE_DIR" ]]; then
    if command -v trash >/dev/null 2>&1; then
      trash "$CAPTURE_DIR"
    else
      echo "note: temporary captures retained at $CAPTURE_DIR (trash command unavailable)" >&2
    fi
  fi
}
trap cleanup EXIT

die() {
  echo "marketing captures: $*" >&2
  exit 1
}

release_version::is_supported "$CAPTURE_RELEASE" || \
  die "VERSION must be a shipped supported release (got: $CAPTURE_RELEASE)"
[[ -d "$SITE_ROOT/.git" || -f "$SITE_ROOT/.git" ]] || \
  die "canonical inspr-site checkout not found at $SITE_ROOT"
[[ -f "$SITE_ROOT/web/scripts/sync-paimos-captures.mjs" ]] || \
  die "site capture publisher is missing at $SITE_ROOT/web/scripts/sync-paimos-captures.mjs"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v ffmpeg >/dev/null 2>&1 || die "ffmpeg is required"
command -v ffprobe >/dev/null 2>&1 || die "ffprobe is required"
command -v node >/dev/null 2>&1 || die "node is required"

RELEASE_COMMIT=$(git -C "$ROOT" rev-list -n 1 "v$CAPTURE_RELEASE" 2>/dev/null || true)
[[ "$RELEASE_COMMIT" =~ ^[0-9a-f]{40}$ ]] || \
  die "v$CAPTURE_RELEASE is not available locally; public captures must come from a shipped tag"
if ! git -C "$ROOT" diff --quiet "v$CAPTURE_RELEASE" -- \
  backend frontend ':(exclude)backend/cmd/dev-fixture-sql'; then
  die "backend/frontend differ from v$CAPTURE_RELEASE; capture the shipped release, not unreleased UI"
fi

HEALTH=$(curl -fsS "$API_URL/api/health") || \
  die "the seeded dev backend is not running; start it with: just dev-up"
HEALTH_VERSION=$(printf '%s' "$HEALTH" | node -e \
  'let s="";process.stdin.on("data",c=>s+=c).on("end",()=>process.stdout.write(JSON.parse(s).version||""))')
if [[ "$HEALTH_VERSION" != "$CAPTURE_RELEASE" && "$HEALTH_VERSION" != "dev" ]]; then
  die "dev backend reports unexpected version $HEALTH_VERSION"
fi
if [[ "$HEALTH_VERSION" == "dev" ]]; then
  echo "→ dev binary confirmed; backend/frontend source matches v$CAPTURE_RELEASE"
fi
curl -fsS "$APP_URL" >/dev/null || \
  die "the seeded Vite app is not running; start it with: just dev-up"

TOKEN_FILE="${PAIMOS_DEV_LOGIN_TOKEN_FILE:-$HOME/Secrets/dev/PAIMOS_DEV_LOGIN_TOKEN.env}"
if [[ -z "${PAIMOS_DEV_LOGIN_TOKEN:-}" ]]; then
  [[ -f "$TOKEN_FILE" ]] || die "dev-login token file not found at $TOKEN_FILE"
  # shellcheck disable=SC1090
  source "$TOKEN_FILE"
fi
[[ -n "${PAIMOS_DEV_LOGIN_TOKEN:-}" ]] || die "PAIMOS_DEV_LOGIN_TOKEN is not set"
export PAIMOS_DEV_LOGIN_TOKEN

"$ROOT/scripts/visual-shot.sh" --bootstrap-only
TOOL="$ROOT/scripts/.visual-tooling"
CAPTURE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/paimos-captures.XXXXXX")

echo "→ polishing the local seeded workspace"
for fixture_sql in \
  demo-polish.sql \
  demo-memory.sql \
  demo-intake-session.sql \
  demo-intake-events.sql; do
  (cd "$ROOT/backend" && DATA_DIR="$ROOT/data" PAIMOS_ENV=development \
    go run ./cmd/dev-fixture-sql < "$ROOT/scripts/marketing/$fixture_sql")
done

echo "→ capturing Paimos v$CAPTURE_RELEASE at 1600×1000 @2x"
OUT_DIR="$CAPTURE_DIR" NODE_PATH="$TOOL/node_modules" \
  node "$ROOT/scripts/marketing/capture-views.cjs"
OUT_DIR="$CAPTURE_DIR" NODE_PATH="$TOOL/node_modules" PAIMOS_DB="$ROOT/data/paimos.db" \
  node "$ROOT/scripts/marketing/capture-intake.cjs"
OUT_DIR="$CAPTURE_DIR" NODE_PATH="$TOOL/node_modules" \
  node "$ROOT/scripts/marketing/capture-loops.cjs"

echo "→ transcoding product loops to fast-start H.264"
for loop in loop-issue-workbench loop-search-navigate; do
  trim_start=$(node -e \
    'const fs=require("fs");const [path,name]=process.argv.slice(1);const data=JSON.parse(fs.readFileSync(path));process.stdout.write(String(data.recordings[name].trimStartSeconds));' \
    "$CAPTURE_DIR/capture-loops.json" "$loop")
  ffmpeg -hide_banner -loglevel error -y \
    -ss "$trim_start" -i "$CAPTURE_DIR/$loop.webm" \
    -vf 'fps=24,scale=1280:800:flags=lanczos,format=yuv420p' \
    -an -c:v libx264 -preset slow -crf 26 -profile:v main -level 4.0 \
    -movflags +faststart "$CAPTURE_DIR/$loop.mp4"
  echo "  ✓ $loop.mp4"
done

echo "→ publishing captures into $SITE_ROOT"
node "$SITE_ROOT/web/scripts/sync-paimos-captures.mjs" \
  --capture-dir "$CAPTURE_DIR" \
  --release "$CAPTURE_RELEASE" \
  --source-commit "$RELEASE_COMMIT"

echo "✓ one-command capture refresh complete: Paimos v$CAPTURE_RELEASE"
