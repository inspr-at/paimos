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
# Release 128 is the last published binary below the account_use_v1 floor.
# Keep this negative probe independent of the moving latest-release check:
# release 129 and newer correctly enter activated tenants with that capability.
floor_tag=v261009095632.0.0
floor_digest=sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c
probe_mode="${3:-compatibility}"
case "$probe_mode" in
  compatibility) ;;
  account-use-floor)
    [[ "$tag" = "$floor_tag" && "$digest" = "$floor_digest" ]] || {
      echo 'Account-use floor probe requires the pinned below-floor release' >&2; exit 1;
    } ;;
  *) echo 'Expected compatibility or account-use-floor probe mode' >&2; exit 1 ;;
esac
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
mkdir -p tmp
tmp="$(mktemp -d "$root/tmp/migration-compat.XXXXXXXX")"
suffix="${tmp##*.}"
db="aeon-compat-db-$suffix"
app="aeon-compat-app-$suffix"
network="aeon-compat-$suffix"
image="ghcr.io/inspr-at/aeon@$digest"
# AEON-1051's entry refusal applies to binaries below the account-use floor.
# Release 129 and later advertise that capability, so keep the last published
# below-floor image as a separate immutable counterexample. Its release body
# records this digest and source commit 2beba30ed75f68a6880ce0427fdc71c8d881fb76.
# The latest release still runs every ordinary compatibility read below.
floor_image="ghcr.io/inspr-at/aeon@$floor_digest"
cleanup() {
  docker container rm -fv "$app" "$db" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  if command -v trash >/dev/null 2>&1; then trash "$tmp";
  else echo "Disposable probe state left in $tmp (trash unavailable)" >&2; fi
}
trap cleanup EXIT

pull_previous() {
  echo "Previous published release: $tag"
  echo "Previous registry image: $image"
  docker pull --platform linux/amd64 "$image"
  # Pull the release-note digest, then resolve its local config ID for boots.
  image_id="$(docker image ls --quiet --no-trunc "$image" | sort -u)"
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Expected one immutable image ID' >&2; exit 1; }
  echo "Previous image: $image_id"
}
pull_previous
previous_tag="$tag"
previous_image="$image"
previous_image_id="$image_id"
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

# Verify the immutable pre-capability binary and its seeded reads before
# activation. Exact SQLSTATE/entry, empty/populated pool and background-write
# refusal assertions remain mandatory in the separate disposable database.
if [[ "$probe_mode" = account-use-floor ]]; then
  python3 scripts/migration-compat-probe.py account-use --base "$base" --state "$tmp/state.json" --version "${tag#v}" --database-container "$db"
else
  # Use a fresh disposable database for the negative fixture. A capable latest
  # release must never be made to look incapable, or counted as a refusal.
  cleanup
  trap - EXIT
  echo "Account-use rollback boundary release: $floor_tag"
  bash scripts/migration-compat.sh "$floor_tag" "$floor_digest" account-use-floor
fi
floor_image_id="$(docker image ls --quiet --no-trunc "$floor_image" | sort -u)"
[[ "$floor_image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Expected one immutable rollback boundary image ID' >&2; exit 1; }
echo "Account-use rollback boundary passed: $floor_tag on the candidate schema"
echo "Migration compatibility passed: latest-release reads and below-floor account-use boundary"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf 'Previous release %s; registry image %s; loaded image %s served health, ready, SPA and authenticated read APIs after candidate migrations. Account-use refusal boundary %s; registry image ghcr.io/inspr-at/aeon@%s; loaded image %s passed the activated legacy probes.\n' \
    "$previous_tag" "$previous_image" "$previous_image_id" "$floor_tag" "$floor_digest" "$floor_image_id" >> "$GITHUB_STEP_SUMMARY"
fi
