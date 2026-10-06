#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Release image gate. The csb1 service contract is in nixcfg/hosts/csb1/docker/compose-spec.nix.
# Prod mode checks startup, mounted secrets, database, UID, health and headers.
# Dev mode is used only for authenticated upload and quote API calls: live OIDC
# cannot be exercised by an isolated, disposable Postgres fixture.
set -euo pipefail

for tool in docker curl python3 openssl; do
  command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 1; }
done

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
# Docker Desktop/Colima share the home tree but need not share macOS /tmp.
tmp_root="${AEON_SMOKE_TMP_ROOT:-$HOME/.cache}"
mkdir -p "$tmp_root"
tmp="$(mktemp -d "$tmp_root/aeon-smoke.XXXXXXXX")"
suffix="$(openssl rand -hex 5)"
db="aeon-smoke-db-$suffix"
app="aeon-smoke-app-$suffix"
network="aeon-smoke-$suffix"
volume="aeon-smoke-files-$suffix"
# CI loads a cached build and passes its immutable image ID. Standalone runs
# still build their own disposable image. Never remove a caller-owned image.
image="${AEON_SMOKE_IMAGE:-aeon-smoke:$suffix}"
cleanup() {
  docker container rm -f "$app" "$db" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  if [[ -z "${AEON_SMOKE_IMAGE:-}" ]]; then
    docker image rm "$image" >/dev/null 2>&1 || true
  fi
  if command -v trash >/dev/null 2>&1; then
    trash "$tmp"
  else
    echo "Temporary fixture left at $tmp (trash is unavailable)" >&2
  fi
}
trap cleanup EXIT

mkdir -p "$tmp/files" "$tmp/secrets" "$tmp/initdb" "$tmp/public"
openssl rand -hex 32 > "$tmp/secrets/db-super"
openssl rand -hex 32 > "$tmp/secrets/db-app"
openssl rand -hex 32 > "$tmp/secrets/session"
openssl rand -hex 32 > "$tmp/secrets/messaging"
cat > "$tmp/initdb/10-aeon.sh" <<'INITDB'
#!/bin/sh
set -eu
IFS= read -r app_password < /run/secrets/db-app
psql -v ON_ERROR_STOP=1 -U postgres -d postgres -v pw="$app_password" <<'SQL'
CREATE ROLE aeon LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'pw';
CREATE DATABASE aeon OWNER aeon;
SQL
psql -v ON_ERROR_STOP=1 -U postgres -d aeon -c 'CREATE EXTENSION IF NOT EXISTS vector' >/dev/null
unset app_password
INITDB
chmod 755 "$tmp/initdb/10-aeon.sh"

# Docker performs the ownership change so macOS and Linux use the same UID/GID.
docker run --rm -v "$tmp:/smoke" alpine:3.24 sh -c \
  'chown -R 65532:65532 /smoke/files /smoke/secrets && chmod 750 /smoke/files && chmod 755 /smoke/secrets && chmod 444 /smoke/secrets/*'
files_mount="$tmp/files"
if [[ "$(docker run --rm -v "$files_mount:/data/files" alpine:3.24 stat -c '%u:%g:%a' /data/files)" != 65532:65532:750 ]]; then
  # Colima's macOS file sharing does not preserve chown; a Docker volume is a
  # real directory on its Linux host. CI keeps the csb1-style bind mount.
  echo 'Bind mount cannot preserve UID 65532; using Docker host directory'
  docker volume create "$volume" >/dev/null
  docker run --rm -v "$volume:/data/files" alpine:3.24 sh -c \
    'chown 65532:65532 /data/files && chmod 750 /data/files'
  files_mount="$volume"
fi

if [[ -n "${AEON_SMOKE_IMAGE:-}" ]]; then
  echo 'Using prebuilt release image for smoke gate'
else
  echo 'Building release image for smoke gate'
  node scripts/assemble-image.mjs "$image"
fi
docker run --rm --entrypoint /bin/sh "$image" -c '
  test -s /usr/share/doc/aeon/NOTICE &&
  apk list --installed chromium | grep -Eq "^chromium-152[.]0[.]7977[.]82-r0 .*[(]BSD-3-Clause[)] \\[installed\\]$" &&
  apk list --installed tini | grep -Eq "^tini-0[.]19[.]0-r3 .*[(]MIT[)] \\[installed\\]$"
' || { echo 'runtime quote dependency notice or license check failed' >&2; exit 1; }
echo 'runtime: quote dependency pins, licenses and NOTICE OK'
docker network create "$network" >/dev/null
docker run -d --name "$db" --network "$network" \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD_FILE=/run/secrets/db-super \
  -v "$tmp/secrets:/run/secrets:ro" -v "$tmp/initdb:/docker-entrypoint-initdb.d:ro" \
  pgvector/pgvector:0.8.6-pg18 >/dev/null
ready=0
for _ in {1..45}; do
  if docker exec "$db" pg_isready -h 127.0.0.1 -U postgres -d aeon >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if (( ready == 0 )); then echo 'Postgres did not become ready' >&2; exit 1; fi

start_app() {
  local mode="$1"
  docker run -d --name "$app" --network "$network" --memory 256m \
    -p 127.0.0.1::8080 \
    -e "AEON_ENV=$mode" -e AEON_ADDR=:8080 -e AEON_FILES_DIR=/data/files \
    -e AEON_PUBLIC_URL=http://localhost:8080 \
    -e AEON_DATABASE_URL='postgres://aeon@aeon-smoke-db-'"$suffix"':5432/aeon?sslmode=disable' \
    -e AEON_DATABASE_PASSWORD_FILE=/run/secrets/db-app \
    -e AEON_SESSION_KEY_FILE=/run/secrets/session \
    -e AEON_MESSAGING_KEY_FILE=/run/secrets/messaging \
    -e AEON_OIDC_ISSUER=https://auth.invalid \
    -e AEON_OIDC_CLIENT_ID=smoke-client \
    -e AEON_BOOTSTRAP_ADMIN_EMAIL=smoke@example.invalid \
    -v "$files_mount:/data/files" \
    -v "$tmp/secrets/db-app:/run/secrets/db-app:ro" \
    -v "$tmp/secrets/session:/run/secrets/session:ro" \
    -v "$tmp/secrets/messaging:/run/secrets/messaging:ro" \
    "$image" >/dev/null
  local mapped
  mapped="$(docker port "$app" 8080/tcp)"
  base="http://$mapped"
  local healthy=0
  for _ in {1..45}; do
    if curl -fsS --max-time 2 "$base/api/health" > "$tmp/health.json" 2>/dev/null && \
      python3 -c 'import json,sys; h=json.load(open(sys.argv[1])); assert h == {"status":"ok","db":"ok"}' "$tmp/health.json"; then
      healthy=1
      break
    fi
    if ! docker exec "$app" id -u >/dev/null 2>&1; then break; fi
    sleep 1
  done
  if (( healthy == 0 )); then
    echo "$mode image health failed; last container log lines:" >&2
    docker logs --tail 40 "$app" >&2 2>&1 || true
    exit 1
  fi
  [[ "$(docker exec "$app" id -u)" == 65532 ]] || { echo 'runtime UID is not 65532' >&2; exit 1; }
  [[ "$(docker exec "$app" id -g)" == 65532 ]] || { echo 'runtime GID is not 65532' >&2; exit 1; }
  [[ "$(docker exec "$app" stat -c '%u:%g:%a' /data/files)" == 65532:65532:750 ]] || {
    echo 'files mount owner or mode differs from csb1' >&2; exit 1;
  }
  echo "$mode: health and runtime identity OK"
}

start_app prod
SMOKE_BASE="$base" python3 - <<'PY'
import os
import urllib.request
u = os.environ['SMOKE_BASE']
with urllib.request.urlopen(u + '/') as response:
    assert response.status == 200
    assert response.headers['Content-Security-Policy'] == "default-src 'self'; img-src 'self' blob: data:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
    assert response.headers['X-Content-Type-Options'] == 'nosniff'
    assert response.headers['X-Frame-Options'] == 'DENY'
    assert response.headers['Referrer-Policy'] == 'no-referrer'
    assert b'<html' in response.read().lower()
print('prod: embedded web and security headers OK')
PY
docker container rm -f "$app" >/dev/null

start_app dev
SMOKE_BASE="$base" SMOKE_QUOTE_HTML="$tmp/quote.html" SMOKE_PUBLIC_PDF="$tmp/public/quote.pdf" python3 - <<'PY'
import html
import http.cookiejar
import io
import json
import os
import struct
import urllib.error
import urllib.request
import uuid
import zlib

base = os.environ['SMOKE_BASE']
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def call(method, path, body=None, content_type='application/json', headers=None):
    if isinstance(body, (dict, list)):
        body = json.dumps(body).encode()
    request = urllib.request.Request(base + path, data=body, method=method, headers=headers or {})
    if body is not None:
        request.add_header('Content-Type', content_type)
    try:
        with opener.open(request, timeout=50) as response:
            payload = response.read()
            if response.headers.get_content_type() == 'application/json':
                return json.loads(payload)
            return payload
    except urllib.error.HTTPError as error:
        raise AssertionError(f'{method} {path}: HTTP {error.code}: {error.read(300)!r}') from error

def multipart(fields):
    boundary = 'aeon-smoke-' + uuid.uuid4().hex
    data = io.BytesIO()
    for name, (filename, content_type, value) in fields.items():
        data.write(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"'.encode())
        if filename:
            data.write(f'; filename="{filename}"'.encode())
        data.write(f'\r\nContent-Type: {content_type}\r\n\r\n'.encode())
        data.write(value)
        data.write(b'\r\n')
    data.write(f'--{boundary}--\r\n'.encode())
    return data.getvalue(), 'multipart/form-data; boundary=' + boundary

def png():
    def chunk(kind, data):
        return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data) & 0xffffffff)
    scanlines = b'\x00' + b'\xff\x00\x00\xff' * 2
    scanlines += b'\x00' + b'\x00\x00\xff\xff' * 2
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2, 2, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(scanlines)) + chunk(b'IEND', b'')

call('POST', '/api/auth/dev-login', {'email': 'smoke@example.invalid'})
kinds = {item['slug']: item['id'] for item in call('GET', '/api/kinds')['items']}
for slug, prefix in [('organisation', 'ORG'), ('quote', 'QUO')]:
    if slug not in kinds:
        kinds[slug] = call('POST', '/api/kinds', {'slug': slug, 'label': slug.title(),
            'short_prefix': prefix, 'icon': 'folder', 'field_schema': {'type': 'object', 'properties': {}}})['id']
project = call('POST', '/api/nodes', {'kind_id': kinds['project'], 'title': 'Smoke project'})
content = b'AEON image smoke attachment\n'
body, content_type = multipart({'file': ('smoke.txt', 'text/plain', content)})
attachment = call('POST', f"/api/nodes/{project['id']}/attachments", body, content_type)[0]
assert call('GET', f"/api/attachments/{attachment['id']}/content") == content
print('dev: attachment upload and download OK')

# Every web action is gated by can(), which reads /api/me/permissions. If the
# authz module is not mounted the whole UI turns read-only (P1, 2026-09-26).
perms = call('GET', '/api/me/permissions')
assert 'nodes.write' in perms['workspace']['permissions'], perms
members = call('GET', '/api/members')
assert members['people'], members
print('dev: permissions and members served OK')

body, content_type = multipart({'file': ('avatar.png', 'image/png', png()),
    'crop': ('', 'application/json', b'{"x":0,"y":0,"size":2}')})
# The runtime image must resolve IANA time zones (tzdata is embedded in the binary).
tz = call('PATCH', '/api/me/profile', {'timezone': 'Europe/Vienna'})
assert tz.get('timezone') == 'Europe/Vienna', tz
profile = call('POST', '/api/me/avatar', body, content_type)
assert profile['avatar_hashes']['32']
avatar = call('GET', f"/api/people/{profile['principal_id']}/avatar/32")
assert avatar.startswith(b'\x89PNG\r\n\x1a\n')
print('dev: avatar crop and stored variant OK')

catalog = {item['id']: item for item in call('GET', '/api/plugins')}
for plugin in ('business_costs', 'business_crm', 'business_quotes'):
    item = catalog[plugin]
    call('PUT', f'/api/plugins/{plugin}/installation', {'manifest_digest_sha256': item['digest_sha256'],
        'enabled': True, 'permissions': item['permissions']})
call('PATCH', '/api/quotes/settings', {'expected_revision': 0, 'numbering_time_zone': 'Europe/Vienna',
    'default_currency': 'EUR', 'sender': {'company': 'Smoke fixture', 'street': 'Test Lane 1',
        'postal_code': '1000', 'city': 'Test City', 'country': 'AT', 'email': 'sender@example.invalid'},
    'defaults': {}, 'layout': {},
    'smtp_confirmation_enabled': False})
org = call('POST', '/api/nodes', {'kind_id': kinds['organisation'], 'title': 'Smoke customer'})
quote = call('POST', '/api/quotes', {'title': 'Smoke quote', 'customer_org_node_id': org['id']})
quote_id = quote['quote_node_id']
draft = call('GET', f'/api/quotes/{quote_id}/draft')
document = draft['document']
document['recipient']['address'] = 'Customer Lane 2'
document['recipient']['email'] = 'customer@example.invalid'
document['positions'] = [{'id': str(uuid.uuid4()), 'pricing_source': 'manual', 'short_text': 'Smoke service',
    'long_text': 'Runtime PDF check', 'quantity': '1.00', 'unit_label': 'item',
    'unit_price_cents': 100, 'total_cents': 100, 'currency': 'EUR'}]
receipt = call('PATCH', f'/api/quotes/{quote_id}/draft', {'client_session_id': str(uuid.uuid4()),
    'mutation_id': str(uuid.uuid4()), 'writer_version': 1, 'document': document},
    headers={'If-Match': f'"qd-{draft["draft_revision"]}"'})
quote = call('GET', f'/api/quotes/{quote_id}')
issued = call('POST', f'/api/quotes/{quote_id}/finalize', {'expected_quote_revision': quote['revision'],
    'expected_draft_revision': receipt['acknowledged_revision'],
    'expected_document_sha256': receipt['document_sha256']})
assert issued['state'] == 'issued'
version = call('GET', f'/api/quotes/{quote_id}/versions/1')
# Finalization allocates the customer capability in the same tenant transaction.
link = call('GET', f'/api/quotes/{quote_id}/versions/1/public-link')
assert link['path'].startswith('/offers/')
public_api = '/api/public/quotes/' + link['path'].removeprefix('/offers/')
# A fresh opener carries no dev-login session. The image connects as aeon,
# the non-superuser role used by the production service.
with urllib.request.urlopen(base + public_api, timeout=50) as response:
    assert response.status == 200
    projection = json.load(response)
    assert projection['version'] == 1
    assert projection['content_sha256'] == version['content_sha256']
with urllib.request.urlopen(base + public_api + '/pdf', timeout=50) as response:
    assert response.status == 200
    assert response.headers.get_content_type() == 'application/pdf'
    pdf_bytes = response.read()
    assert pdf_bytes.startswith(b'%PDF-')
    with open(os.environ['SMOKE_PUBLIC_PDF'], 'wb') as output:
        output.write(pdf_bytes)
print('dev: anonymous public quote projection and PDF OK')
with open(os.environ['SMOKE_QUOTE_HTML'], 'w', encoding='utf-8') as output:
    output.write('<!doctype html><meta charset="utf-8"><title>Quote smoke</title>'
        '<h1>' + html.escape(document['title']) + '</h1>'
        '<p>Smoke service: EUR 1.00</p>')
print('dev: issued quote fixture OK')
PY

# A disposable Poppler reader inspects the actual public PDF bytes. The
# release image stays small; only this synthetic PDF is mounted into the reader.
docker run --rm -v "$tmp/public:/smoke:ro" alpine:3.24 sh -c '
  apk add --no-cache poppler-utils >/dev/null &&
  pdfinfo /smoke/quote.pdf | grep -E "^Page size:.*A4" >/dev/null &&
  pdftotext /smoke/quote.pdf - | grep -F "Smoke quote" >/dev/null &&
  pdftotext /smoke/quote.pdf - | grep -F "Smoke service" >/dev/null
' || { echo 'public quote PDF text or A4 geometry check failed' >&2; exit 1; }
echo 'dev: public quote PDF text and A4 geometry OK'

# Print the issued quote fixture inside the exact runtime image, as its USER.
# The public capability endpoint has separate authorization/selector semantics;
# this check targets Chromium launch and file access under the production UID.
docker cp "$tmp/quote.html" "$app:/data/files/smoke-quote.html"
docker exec "$app" sh -c '
  browser="$(command -v chromium || command -v chromium-browser)"
  "$browser" --headless --no-sandbox --disable-gpu --disable-dev-shm-usage \
    --no-first-run --print-to-pdf=/data/files/smoke-quote.pdf \
    file:///data/files/smoke-quote.html >/dev/null 2>&1
  test "$(head -c 5 /data/files/smoke-quote.pdf)" = %PDF-
  test "$(stat -c %s /data/files/smoke-quote.pdf)" -gt 1000
'
echo 'dev: issued quote rendered by Chromium to PDF OK'

echo 'Image smoke passed'
