#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# CI/Linux only: exercise the operator's actual compose file with disposable data.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
for tool in docker python3 openssl; do
  command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 1; }
done
export AEON_IMAGE="${AEON_IMAGE:?Supply a pinned release image}"
export AEON_DATA_DIR
AEON_DATA_DIR="$(mktemp -d "${RUNNER_TEMP:-/tmp}/aeon-compose.XXXXXXXX")"
export AEON_PUBLIC_URL=http://localhost:8080
export AEON_HTTP_PORT=8080
export AEON_OIDC_ISSUER=http://oidc:8080
export AEON_OIDC_CLIENT_ID=compose-smoke
export AEON_BOOTSTRAP_ADMIN_EMAIL=admin@example.invalid
export AEON_BOOTSTRAP_TENANT_SLUG=workspace
export AEON_BOOTSTRAP_TENANT_NAME='Compose smoke'
project="aeon-smoke-$(openssl rand -hex 5)"
compose=(docker compose -p "$project" -f deploy/compose/compose.yaml -f deploy/compose/ci/compose.yaml)
cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  # The ephemeral CI runner discards this directory, including fixture secrets.
}
trap cleanup EXIT

# Check Docker's actual health state without inspecting container environments.
assert_health() {
  python3 - "$1" "$2" "${compose[@]}" <<'PY'
import subprocess
import sys
import time

service, expected, *compose = sys.argv[1:]
deadline = time.monotonic() + 120
while True:
    result = subprocess.run(compose + ['ps', '--all', '--format', '{{.Health}}', service],
                            check=True, capture_output=True, text=True, timeout=10)
    health = result.stdout.strip()
    if health == expected:
        print(f'compose: {service} health is {expected}')
        break
    if time.monotonic() >= deadline:
        raise AssertionError(f'{service} health is {health!r}, expected {expected}')
    time.sleep(2)
PY
}

sudo bash deploy/compose/prepare.sh "$AEON_DATA_DIR"
if sudo bash deploy/compose/prepare.sh "$AEON_DATA_DIR"; then
  echo 'Preparation overwrote existing state' >&2
  exit 1
fi

"${compose[@]}" config --quiet
# Exercise incomplete initialization before Aeon creates vector-dependent tables.
"${compose[@]}" up -d --wait --wait-timeout 180 postgres
"${compose[@]}" exec -T postgres psql -U postgres -d aeon -v ON_ERROR_STOP=1 -c 'DROP EXTENSION vector'
assert_health postgres unhealthy
"${compose[@]}" exec -T postgres psql -U postgres -d aeon -v ON_ERROR_STOP=1 -c 'CREATE EXTENSION vector'
assert_health postgres healthy
# Only this disposable CI database exists yet; FORCE closes any concurrent probe.
"${compose[@]}" exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c 'DROP DATABASE aeon WITH (FORCE)'
# This still succeeds: pg_isready alone does not establish database existence.
"${compose[@]}" exec -T postgres pg_isready -h 127.0.0.1 -U postgres -d aeon
assert_health postgres unhealthy
"${compose[@]}" exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c 'CREATE DATABASE aeon OWNER aeon'
"${compose[@]}" exec -T postgres psql -U postgres -d aeon -v ON_ERROR_STOP=1 -c 'CREATE EXTENSION vector'
assert_health postgres healthy
"${compose[@]}" up -d --wait --wait-timeout 180
"${compose[@]}" exec -T aeon sh -c '
  test "$(id -u):$(id -g)" = 65532:65532
  test "$(stat -c %u:%g:%a /data/files)" = 65532:65532:750
  test -r /run/secrets/session && test -r /run/secrets/messaging
  printf compose-persistence > /data/files/compose-smoke.txt
'
"${compose[@]}" exec -T postgres psql -U postgres -d aeon -v ON_ERROR_STOP=1 <<'SQL'
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='aeon' AND
    (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole)) OR
    NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='aeon' AND rolcanlogin) THEN
    RAISE EXCEPTION 'unsafe or absent application role';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname='vector') THEN
    RAISE EXCEPTION 'pgvector is absent';
  END IF;
END $$;
SQL

python3 - <<'PY'
import json
import urllib.error
import urllib.parse
import urllib.request

base = 'http://localhost:8080'
with urllib.request.urlopen(base + '/api/health', timeout=10) as response:
    assert json.load(response) == {'status': 'ok', 'db': 'ok'}
with urllib.request.urlopen(base + '/', timeout=10) as response:
    assert response.status == 200 and b'<html' in response.read().lower()

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

opener = urllib.request.build_opener(NoRedirect())
try:
    opener.open(base + '/api/auth/login?tenant=workspace', timeout=10)
    raise AssertionError('sign-in did not redirect')
except urllib.error.HTTPError as response:
    assert response.code == 302
    target = urllib.parse.urlsplit(response.headers['Location'])
    query = urllib.parse.parse_qs(target.query)
    assert target.scheme == 'http' and target.netloc == 'oidc:8080'
    assert target.path == '/authorize'
    assert query['client_id'] == ['compose-smoke']
    assert query['redirect_uri'] == [base + '/api/auth/callback']
    assert query['response_type'] == ['code']
    assert query['code_challenge_method'] == ['S256']
    assert query['state'][0] and query['nonce'][0] and query['code_challenge'][0]
    assert set(query['scope'][0].split()) == {'openid', 'profile', 'email'}
print('compose: health, embedded web and OIDC/PKCE sign-in redirect OK')
PY

# Liveness stays HTTP 200 during an outage; readiness and Docker health must fail.
"${compose[@]}" stop postgres
assert_health aeon unhealthy
python3 - <<'PY'
import json
import urllib.error
import urllib.request

base = 'http://localhost:8080'
with urllib.request.urlopen(base + '/api/health', timeout=10) as response:
    assert response.status == 200
    assert json.load(response) == {'status': 'ok', 'db': 'down'}
try:
    urllib.request.urlopen(base + '/api/ready', timeout=10)
    raise AssertionError('readiness succeeded while Postgres was stopped')
except urllib.error.HTTPError as response:
    assert response.code == 503
    body = json.load(response)
    assert body.get('status') == 'unavailable'
    assert body.get('reason') in {'database_unavailable', 'not_accepting'}
    assert set(body) <= {'status', 'reason', 'pool', 'detail'}
PY
"${compose[@]}" start postgres
assert_health postgres healthy
assert_health aeon healthy

# Recreate the app and restart the database without discarding persistent data.
"${compose[@]}" stop aeon
"${compose[@]}" restart postgres
"${compose[@]}" up -d --no-deps --wait --wait-timeout 90 postgres
"${compose[@]}" up -d --force-recreate --no-deps --wait --wait-timeout 180 aeon
"${compose[@]}" exec -T aeon sh -c 'test "$(cat /data/files/compose-smoke.txt)" = compose-persistence'
echo 'compose: non-superuser database, pgvector, UID 65532 and persisted files OK'
