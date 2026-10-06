# Self-hosting PAIMOS AEON

Aeon runs as one server container with its web app embedded, Postgres 18 with
pgvector, and an external OpenID Connect (OIDC) provider. The reference stack is
[deploy/compose/compose.yaml](../deploy/compose/compose.yaml). It does not depend
on a particular hostname or fleet host. Its database initialization follows
[scripts/smoke-image.sh](../scripts/smoke-image.sh): the application connects as
a database owner that is **not** a superuser and cannot bypass row-level security.

## Prerequisites

Use a dedicated Linux Docker host with Docker Engine, the Docker Compose v2
plugin (`docker compose`), Bash, OpenSSL, and curl. The published server image
targets Linux amd64. Have storage for the database, uploaded files, and backups;
size CPU/RAM for your workload, including Chromium used for quote PDFs.

Provide an HTTPS hostname and a reverse proxy on the same host, plus an OIDC
provider reachable by both the server and people's browsers. Zitadel is the
project's provider; the client uses standard OIDC discovery and authorization
code flow with PKCE. Register a **public client**, with no client secret, allow
the `openid profile email` scopes, and register this exact redirect URI:

```text
https://work.example.org/api/auth/callback
```

Use your own hostname. `AEON_OIDC_ISSUER` must be the provider's issuer URL,
matching the discovery document's `issuer`, rather than its authorization URL.
The initial administrator must have the email configured below. Other people
need tenant membership or an invitation; arbitrary OIDC accounts do not gain
workspace access. Use a trusted provider and verified email addresses.

## Pin the release and configure the installation

Obtain a checkout or archive of the corresponding
[Aeon release](https://github.com/inspr-at/paimos/releases) and open a Bash shell
at its root. Follow the commands in that same shell. Select an explicit version
from the release notes; **no `latest` tag is published**. The version example
below is a published release, not an automatically updated recommendation.
For reproducibility, prefer the image digest recorded by the release.

```bash
export AEON_IMAGE=ghcr.io/inspr-at/aeon:261001072608.0.0
# Alternatively: ghcr.io/inspr-at/aeon@sha256:<digest-from-release>
export AEON_DATA_DIR=/srv/aeon
export AEON_PUBLIC_URL=https://work.example.org
export AEON_OIDC_ISSUER=https://auth.example.org
export AEON_OIDC_CLIENT_ID=your-public-client-id
export AEON_BOOTSTRAP_ADMIN_EMAIL=you@example.org
export AEON_BOOTSTRAP_TENANT_SLUG=workspace
export AEON_BOOTSTRAP_TENANT_NAME='Your workspace'
export AEON_HTTP_PORT=8080
export AEON_COMPOSE="$PWD/deploy/compose/compose.yaml"
dc() { docker compose -f "$AEON_COMPOSE" "$@"; }
```

These exports are configuration, not secret values. Persist them in your
operator's service configuration so future restart/upgrade commands use the
same values and Compose project name (`aeon`). Do not put credentials into a
database URL or a Compose environment variable.

The GHCR image was checked anonymously on 2026-10-01: the pull-token request
and manifest HEAD for `261001072608.0.0` both returned HTTP 200. No registry
credentials were used and no visibility setting was changed. Registry access
can change; `dc pull` below is the install-time verification. If denied, check
the selected release and package access rather than substituting `latest`.

## Prepare durable files and start

On a **fresh** host directory, run the one-time preparation script. It refuses
existing `files` or `secrets` directories to avoid replacing persistent keys.

```bash
sudo bash deploy/compose/prepare.sh "$AEON_DATA_DIR"
dc config --quiet
dc pull
dc up -d --wait --wait-timeout 180
curl --fail --silent --show-error http://127.0.0.1:8080/api/health
curl --fail --silent --show-error http://127.0.0.1:8080/api/ready
```

If you changed `AEON_HTTP_PORT`, use that port in both requests. The expected
health body is `{"status":"ok","db":"ok"}`; readiness returns
`{"status":"ready"}`. Compose probes `/api/ready`, which returns HTTP 503 when
the database cannot be reached or the app is not accepting requests. `/api/health`
is a liveness report: it returns HTTP 200 even when its `db` field is `down`.
The server applies embedded database migrations automatically before listening.
Postgres initialization creates the `aeon` database, a non-superuser `aeon` login,
and the vector extension on the first start of an empty database volume. It does
not rerun for an existing volume.
The Postgres healthcheck requires TCP acceptance, a query against the `aeon`
database, and the installed `vector` extension.

Preparation generates four independent 32-byte random hex secrets without
printing them. `db-super` is for Postgres initialization and maintenance;
`db-app` is shared by database initialization and Aeon's password-file reader.
The `session` key signs sessions and protects persistent host credentials;
`messaging` encrypts messaging credentials and also derives the default link
vault key. Keep both stable across restarts and upgrades. Changing a secret file
is not a database password change or a supported encryption-key rotation.

File-backed Compose secrets are bind mounts; Compose cannot remap their host
ownership through `uid`, `gid`, or `mode`. The script sets the host's secret
parent to root-only mode `0700`, database secret files to `0444` for their two
different container readers, and session/messaging to `65532:65532`, mode `0400`.
Each container receives only its required files under `/run/secrets`; Aeon never
receives `db-super`. Uploaded files live at `$AEON_DATA_DIR/files`, owned by
`65532:65532`, mode `0750`. Preserve this numeric ownership when restoring them.
See Docker's [Compose secret mount rules](https://docs.docker.com/reference/compose-file/services/#secrets).

Postgres data uses the `aeon_postgres-data` named volume, mounted at
`/var/lib/postgresql`, the parent of Postgres 18's versioned data directory.
See the [Postgres image's storage documentation](https://github.com/docker-library/docs/blob/master/postgres/README.md#pgdata).
No database port is published. The app port binds only to `127.0.0.1`; the
reference expects a host reverse proxy. A containerized proxy needs an explicit
shared-network configuration rather than accessing its own loopback interface.

## HTTPS and first sign-in

Configure your host reverse proxy to forward `https://work.example.org` to
`http://127.0.0.1:8080`. Preserve the original host and HTTPS forwarding headers.
Support server-sent events without buffering and allow long-lived connections;
set request-size limits appropriate for attachments. Keep the public URL equal
to the external HTTPS origin, with no trailing slash. For example, an nginx
location inside your existing TLS virtual host can use:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

Open the public URL and sign in through your OIDC provider using the bootstrap
administrator email. The bootstrap workspace is created during startup; its
first matching OIDC sign-in creates the administrator. Add members through
Aeon's membership/invitation controls. Keep `AEON_ENV=prod`; the development
login is not an installation shortcut.

## Back up, upgrade, and recover

Back up the database, file directory, four secrets, and non-secret configuration
as one recoverable installation. Keep secrets in protected, encrypted backups.
For a consistent simple maintenance backup, stop app writes first and use a new,
protected backup directory outside the checkout:

```bash
set -o pipefail
backup_dir=/srv/aeon-backups/$(date -u +%Y%m%dT%H%M%SZ)
sudo install -d -m 700 "$backup_dir"
dc stop aeon
dc exec -T postgres pg_dump -U postgres -d aeon -Fc | sudo tee "$backup_dir/aeon.dump" >/dev/null
sudo tar --numeric-owner -C "$AEON_DATA_DIR" -cpf "$backup_dir/files-and-secrets.tar" files secrets
dc start aeon
```

Check every command's exit status, and record the current image digest and configuration with the backup.
Periodically restore into a separate recovery host to verify the backup.

Personal-data access and erasure requests go through the installation's
operator/workspace owner, who verifies identity and handles the request manually.
The offline `aeon admin dsar export --tenant SLUG --actor-principal-id OWNER_UUID
--person UUID_OR_EMAIL` produces a JSON review packet; free text, files and
third-party information need manual review and supplementation before delivery.
`aeon admin dsar erase` with the same scope and `--dry-run` lists review targets
and hold assessments. Collection uses a read-only snapshot. Before releasing
either packet, a separate transaction records one tenant audit event with the
owner, subject principal reference, operation and counts, without personal
content. Audit failure releases no packet; the event records collection, not
successful delivery. There is no self-service erasure or automatic retention.
Use `--output PATH` for a new mode-0600 file in
a private directory, and include retained copies and backups in the assessment.

`paimos files verify --tenant SLUG` checks the tenant's shared blob store:
attachments (including soft-deleted/history references), avatars and their undo
history, confirmation receipts, and immutable quote profile assets used by
frozen versions. `paimos files gc --tenant SLUG` is a dry run; `--apply` deletes
only unreferenced files at least seven days old. Writers and cleanup serialize
on the same tenant/hash database lock through reference commit or file unlink.
Multi-file transactions reserve their complete blob set in tenant/hash order
before publishing files or appending events, including all profiles in a
showcase apply. The storage guard rejects new out-of-order or post-event locks.
Upgrade every writer and operator CLI before using cleanup: older binaries do
not participate in this protocol. Verify reports pre-existing missing or corrupt
files; cleanup does not repair them.

Before upgrading, read the target release notes, take a verified backup, and
record the old pinned image. Update `AEON_IMAGE` to the new explicit version or
digest, then run `dc pull aeon` and `dc up -d --no-deps --wait --wait-timeout 180
aeon`. Check health, sign-in, and an uploaded file through the public URL.
Do not change the Postgres major version as part of an app-image update.

An app rollback can require restoring the matching pre-upgrade database and
files: migrations run forward automatically and may be incompatible with an
older binary. Stop app writes, restore the backup into a fresh Postgres 18
volume initialized by the same reference stack, restore files and secrets with
their recorded ownership, and select the old exact image. For the database,
start only Postgres with `dc up -d --wait postgres` before restoring, so the app
has not applied migrations to the recovery database.
`dc exec -T postgres pg_restore -U postgres -d aeon --exit-on-error` reads the
dump from stdin; use it only against the fresh recovery database. Verify health,
sign-in and file access before switching traffic. Preserve the previous volume
until recovery is verified. Never use `docker compose down --volumes` on an
installation whose data you intend to retain.

## Troubleshooting and CI evidence

Use `dc ps`, `dc logs --tail 50 aeon`, and `dc logs --tail 50 postgres`. Missing
secret files or wrong permissions fail startup; file upload failures often mean
the host files directory is not writable by UID 65532. If health succeeds but
sign-in fails, check issuer discovery, client ID, redirect URI, bootstrap email,
and the externally visible public URL. Health does not test OIDC authentication.

If first-run initialization fails, restarting Postgres can leave it accepting
connections against a partially initialized volume. The healthcheck remains
unhealthy if the `aeon` database or `vector` extension is missing. Fix the cause
reported by initialization, stop the stack, and preserve the failed volume for
inspection. Recreate the database volume only after confirming it contains no
data to retain, then start against the fresh volume so initialization runs again.
For an installation with existing data, use the verified backup/recovery procedure
above instead of discarding its volume.

The **Self-hosting compose** CI workflow boots this exact Compose stack in prod
mode with a discovery-only mock OIDC container and the pinned published image.
It checks database health, rejection of missing database/extension states,
app health during a database outage and recovery, embedded web, the
authorization-code/PKCE redirect, the non-superuser role, pgvector, UID/GID 65532,
and files surviving app recreation
and a database restart. It runs for relevant guide/Compose/Dockerfile changes
and weekly. The mock cannot issue tokens; full authentication remains covered
by the auth package tests and must be verified with your own provider during
installation. The release image workflow separately builds and tests candidate
Dockerfiles. Do not add the CI-only Compose overlay to an actual installation.

### Work-kind migration (AEON-649)

Migration 1215 replaces the Epic, Ticket and Task kind definitions with Work.
It preserves node keys/content and historical session/Decision Desk identities,
but older binaries that expect the retired kinds are incompatible with the new
database. This package is prepared as part of the work-node chain; the coordinator
must complete the dependent runtime packages before deploying it. Derived
status and workspace naming remain behind AEON-429.

Before that rollout, publish release 122, drain the merge/work queues and stop
writers. Verify an instance-specific pre-migration database/files backup on a
separate recovery host before upgrading; do not restart writers between the
verified backup and migration. The migration refuses schema conflicts or busy
work parents and rolls back atomically. It reports tenant/node identifiers so
schemas can be reconciled and agents can hand over gracefully before retrying.
The local synthetic drill and its retained archive/evidence are documented in
[the migration guide](../internal/db/migrations/README.md); they do not replace
an installation-specific backup. Restore the verified pre-migration backup and
its exact old binary to roll back. Node Undo cannot restore deleted kind definitions.
