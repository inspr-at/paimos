#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Read-only image pull; every container/network belongs to this disposable run.
set -euo pipefail

# OPS-287: GHCR mirror of the Docker Hub image; refresh with mirror-ci-images.yml
pgvector_image="${PGVECTOR_IMAGE:-ghcr.io/inspr-at/paimos-ci/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11}"

for tool in docker python3 go; do
  command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 1; }
done
tag="${1:?usage: bash scripts/migration-compat.sh vYYMMDDhhmmss.0.0 sha256:DIGEST}"
[[ "$tag" =~ ^v[0-9]{12}\.0\.0$ ]] || { echo 'Expected an immutable release tag' >&2; exit 1; }
digest="${2:?Expected the published release image digest}"
[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Expected an immutable image digest' >&2; exit 1; }
# AEON-1051's refusal proof needs a binary without account_use_v1. Release
# 129 and later declare that capability, so releases/latest is no longer a
# below-floor target. Keep release 128's published tag/digest fixed while the
# latest stable image continues to prove ordinary migration compatibility.
below_floor_tag="v261009095632.0.0"
below_floor_digest="sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
mkdir -p tmp
tmp="$(mktemp -d "$root/tmp/migration-compat.XXXXXXXX")"
suffix="${tmp##*.}"
db="aeon-compat-db-$suffix"
app="aeon-compat-app-$suffix"
network="aeon-compat-$suffix"
image="ghcr.io/inspr-at/aeon@$digest"
cleanup() {
  docker container rm -fv "$app" "$db" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  if command -v trash >/dev/null 2>&1; then trash "$tmp";
  else echo "Disposable probe state left in $tmp (trash unavailable)" >&2; fi
}
trap cleanup EXIT

echo "Previous published release: $tag"
echo "Previous registry image: $image"
docker pull --platform linux/amd64 "$image"
# Pull the release-note digest, then resolve its local config ID for both boots.
image_id="$(docker image ls --quiet --no-trunc "$image" | sort -u)"
[[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Expected one immutable image ID' >&2; exit 1; }
echo "Previous image: $image_id"
docker network create "$network" >/dev/null
docker run -d --name "$db" --network "$network" -p 127.0.0.1::5432 \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=aeon \
  "$pgvector_image" >/dev/null
ready=0
for _ in {1..60}; do
  if docker exec "$db" pg_isready -h 127.0.0.1 -U postgres -d postgres >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[[ "$ready" = 1 ]] || { echo 'Disposable Postgres failed to start' >&2; exit 1; }
# Fixture credentials only. The application role cannot bypass tenant RLS.
docker exec -i "$db" psql -U postgres -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
CREATE ROLE aeon LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD 'aeon';
CREATE DATABASE aeon OWNER aeon;
\connect aeon
CREATE EXTENSION vector;
SQL

start_previous() {
  docker run -d --name "$app" --network "$network" --platform linux/amd64 \
    --memory 256m -p 127.0.0.1::8080 \
    -e AEON_ENV=dev -e AEON_ADDR=:8080 -e AEON_FILES_DIR=/tmp/aeon-compat-files \
    -e AEON_PUBLIC_URL=http://localhost:8080 -e AEON_BOOTSTRAP_ADMIN_EMAIL=compat@example.invalid \
    -e "AEON_DATABASE_URL=postgres://aeon:aeon@$db:5432/aeon?sslmode=disable" \
    "$1" >/dev/null
  base="http://$(docker port "$app" 8080/tcp)"
  python3 scripts/migration-compat-probe.py wait-ready --base "$base"
}

start_previous "$image_id"
python3 scripts/migration-compat-probe.py seed --base "$base" --state "$tmp/state.json" --version "${tag#v}"
docker stop --time 30 "$app" >/dev/null
docker container rm "$app" >/dev/null

# Run exactly the candidate's embedded migration path, including Go backfills
# and nontransactional index recovery. No candidate server or image is built.
db_address="$(docker port "$db" 5432/tcp)"
AEON_ENV=dev AEON_DATABASE_URL="postgres://aeon:aeon@$db_address/aeon?sslmode=disable" \
  GOMAXPROCS=2 go run -p 2 ./scripts/migrate-candidate.go
start_previous "$image_id"
python3 scripts/migration-compat-probe.py check --base "$base" --state "$tmp/state.json" --version "${tag#v}"

# Run every exact capability-entry and background assertion against the real
# below-floor binary, after proving its nonactivated reads and version too.
# This is mandatory even when the latest stable binary has the capability.
below_floor_image="ghcr.io/inspr-at/aeon@$below_floor_digest"
echo "Below-floor published release: $below_floor_tag; image: $below_floor_image"
docker pull --platform linux/amd64 "$below_floor_image"
below_floor_image_id="$(docker image ls --quiet --no-trunc "$below_floor_image" | sort -u)"
[[ "$below_floor_image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Expected one immutable below-floor image ID' >&2; exit 1; }
docker stop --time 30 "$app" >/dev/null
docker container rm "$app" >/dev/null
start_previous "$below_floor_image_id"
python3 scripts/migration-compat-probe.py check --base "$base" --state "$tmp/state.json" --version "${below_floor_tag#v}"
python3 scripts/migration-compat-probe.py account-use --base "$base" --state "$tmp/state.json" --version "${below_floor_tag#v}" --database-container "$db"
echo "Migration compatibility passed: $tag on the candidate schema"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf 'Previous release %s; registry image %s; loaded image %s served health, ready, SPA and authenticated read APIs after candidate migrations.\n' \
    "$tag" "$image" "$image_id" >> "$GITHUB_STEP_SUMMARY"
  printf 'Below-floor release %s; registry image %s; loaded image %s passed nonactivated reads and exact activated capability-entry refusals for empty/populated pools and background jobs.\n' \
    "$below_floor_tag" "$below_floor_image" "$below_floor_image_id" >> "$GITHUB_STEP_SUMMARY"
fi
