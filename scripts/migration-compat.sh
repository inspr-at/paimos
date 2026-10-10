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

# Release 128 is the last published binary below the account-use floor.
# Keep this immutable rollback probe after the latest stable gains capability;
# a capable binary must not be expected to fail at principal entry.
# Published release v261009095632.0.0, source 2beba30ed75f68a6880ce0427fdc71c8d881fb76.
account_use_floor_tag=v261009095632.0.0
account_use_floor_digest=sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c

# Pull every required image before allocating disposable probe resources.
# A failed pull must retain its own failure without network or container work.
docker pull --platform linux/amd64 "ghcr.io/inspr-at/aeon@$digest"
if [[ "$tag" != "$account_use_floor_tag" || "$digest" != "$account_use_floor_digest" ]]; then
  docker pull --platform linux/amd64 "ghcr.io/inspr-at/aeon@$account_use_floor_digest"
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
mkdir -p tmp
tmp="$(mktemp -d "$root/tmp/migration-compat.XXXXXXXX")"
suffix="${tmp##*.}"
db="aeon-compat-db-$suffix"
app="aeon-compat-app-$suffix"
network="aeon-compat-$suffix"
cleanup() {
  docker container rm -fv "$app" "$db" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  if command -v trash >/dev/null 2>&1; then trash "$tmp";
  else echo "Disposable probe state left in $tmp (trash unavailable)" >&2; fi
}
trap cleanup EXIT

probe_release() {
  local tag="$1" digest="$2" exercise_floor="$3"
  local image="ghcr.io/inspr-at/aeon@$digest" image_id base db_address ready
  if [[ "$exercise_floor" = 1 ]]; then
    echo "Account-use rollback boundary release: $tag"
  fi
  echo "Previous published release: $tag"
  echo "Previous registry image: $image"
  # Resolve the pulled release-note digest's local config ID for both boots.
  image_id="$(docker image ls --quiet --no-trunc "$image" | sort -u)"
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Expected one immutable image ID' >&2; exit 1; }
  echo "Previous image: $image_id"
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
      "$image_id" >/dev/null
    base="http://$(docker port "$app" 8080/tcp)"
    python3 scripts/migration-compat-probe.py wait-ready --base "$base"
  }

  start_previous
  python3 scripts/migration-compat-probe.py seed --base "$base" --state "$tmp/state.json" --version "${tag#v}"
  docker stop --time 30 "$app" >/dev/null
  docker container rm "$app" >/dev/null

  # Run exactly the candidate's embedded migration path, including Go backfills
  # and nontransactional index recovery. No candidate server or image is built.
  db_address="$(docker port "$db" 5432/tcp)"
  AEON_ENV=dev AEON_DATABASE_URL="postgres://aeon:aeon@$db_address/aeon?sslmode=disable" \
    GOMAXPROCS=2 go run -p 2 ./scripts/migrate-candidate.go
  start_previous
  python3 scripts/migration-compat-probe.py check --base "$base" --state "$tmp/state.json" --version "${tag#v}"
  echo "Previous release reads passed: $tag on the candidate schema"
  if [[ "$exercise_floor" = 1 ]]; then
    # Seeded reads precede activation. Exact SQLSTATE/entry, empty/populated
    # pool and background-write refusal assertions remain mandatory.
    python3 scripts/migration-compat-probe.py account-use --base "$base" --state "$tmp/state.json" --version "${tag#v}" --database-container "$db"
    echo "Account-use rollback boundary passed: $tag on the candidate schema"
  fi
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    printf 'Previous release %s; registry image %s; loaded image %s served health, ready, SPA and authenticated read APIs after candidate migrations.\n' \
      "$tag" "$image" "$image_id" >> "$GITHUB_STEP_SUMMARY"
    if [[ "$exercise_floor" = 1 ]]; then
      printf 'Account-use refusal boundary %s; registry image %s; loaded image %s passed the activated legacy probes.\n' \
        "$tag" "$image" "$image_id" >> "$GITHUB_STEP_SUMMARY"
    fi
  fi
  docker stop --time 30 "$app" >/dev/null
  docker container rm -fv "$app" "$db" >/dev/null
}

docker network create "$network" >/dev/null
if [[ "$tag" = "$account_use_floor_tag" && "$digest" = "$account_use_floor_digest" ]]; then
  probe_release "$tag" "$digest" 1
else
  # Both images seed and migrate their own fresh disposable database.
  probe_release "$tag" "$digest" 0
  probe_release "$account_use_floor_tag" "$account_use_floor_digest" 1
fi
echo "Migration compatibility passed: $tag on the candidate schema"
