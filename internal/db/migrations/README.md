# Database migrations

`internal/db/migrate.go` applies embedded SQL files in filename order, under a
session advisory lock, and records each completed filename in `schema_migrations`.
Normally each file and its migration record share one transaction.

For an index on a hot table, put `-- aeon:no-transaction` on the **first line**
(before the SPDX comment) and use exactly one `CREATE INDEX CONCURRENTLY`
statement per file. `IF NOT EXISTS` is supported. Index and table names must be
unqualified lowercase identifiers. Other statements, including `SET`, and
multiple index builds are rejected in this mode; use separate numbered files.

The runner executes the index build outside a transaction, with a session
`lock_timeout` of five seconds covering both creation and retry cleanup. This
bounds lock acquisition, not the total build duration. The previous timeout is
restored before returning the connection to the pool, including on failure.

The advisory lock covers inspection, creation and recording. On retry, the runner
keeps a valid index on the expected table, drops an invalid partial index with
`DROP INDEX CONCURRENTLY` and rebuilds it, or rejects a same-named index on a
different table. `IF NOT EXISTS` alone cannot recover an invalid index. Only a
successful build or a valid existing index is recorded; an interrupted build or
a crash before recording can safely be retried. Already recorded files are
skipped. Tests in `concurrent_migration_test.go` exercise these recovery paths.

## Expand and contract (AEON-415)

Ship schema changes in two releases. The expansion release adds the replacement
table or column, backfills data, and moves readers and writers while retaining
the old shape. A later release may remove the unused shape. A rename is
destructive too: add and backfill its replacement first. The immediately previous
published binary must still work after every candidate migration, including a
contract migration, so its remaining dependencies must wait another release.

Use a unique four-digit number for each SQL file, even across parallel branches.
Published files are immutable: add a new file instead of editing, renaming or
deleting an applied migration. Historical destructive SQL stays unchanged.

Every new migration containing any statement outside the expand-safe allowlist
must have this standalone line comment in its header, before SQL (after `aeon:no-transaction`
when applicable):

```sql
-- aeon:contract-phase AEON-415 expanded-in=v260930115354.0.0 expansion-migration=0001_tenants.sql
```

Replace the ticket, release and filename with the actual contract ticket,
already published expansion release and its expansion migration. The release
must be an existing calendar Git tag at or before, and ancestral to, the previous
published release. The named SQL file must exist in that tag's tree; a made-up
early version or a release without the expansion fails. CI fetches full tag
history for this read-only verification. Record the expansion/backfill and the
old binary's removed dependency on that ticket. The marker declares the phase; it never
bypasses runtime compatibility checks.

The allowlist covers `CREATE TABLE`, `CREATE INDEX` (including `CONCURRENTLY`),
`CREATE TYPE`, `CREATE FUNCTION`, `CREATE POLICY`, `CREATE TRIGGER`, nullable
`ADD COLUMN` or one with a constant non-null default, `ADD CONSTRAINT … NOT
VALID`, `VALIDATE CONSTRAINT`, `COMMENT ON`, `GRANT`, `INSERT INTO` / `UPDATE`
backfills and `SET LOCAL`. Tables created without `IF NOT EXISTS` may enable and
force tenant RLS in the same file. A nullable column's default must also be constant; function calls
and expressions are not accepted as constant defaults. Replacing a function,
dropping constraints/defaults/policies/indexes, changing types, setting an
existing column `NOT NULL`, renaming, truncating and deleting require a marker.
All `DO` blocks and dynamic `EXECUTE` function bodies require a marker, including
SQL assigned to a variable before execution. The tokenizer keeps comments,
quoted identifiers and string/dollar literals separate from executable SQL.
Every statement must be allowlisted; unknown forms fail closed.

`scripts/migration-policy-baseline.json` explicitly grandfathers migration
numbers through `1043`, the latest released number when this policy was added.
Later releases advance that bound to their latest migration. The two existing,
unreleased main migrations `1047` and `1048` are pinned by exact SHA-256 content
and their source commit, so adoption does not rewrite historical SQL. Changing
either pinned file fails. The static guard still classifies all files, checks
duplicate numbers across the entire directory and enforces published-file
immutability, including below the baseline. New SQL above that bound must meet
the allowlist or supply verified contract evidence.

The `migration-compat` job in `.github/workflows/ci.yml` runs on pull requests,
`merge_group`, main pushes and manual dispatch, always on GitHub-hosted Linux
with read-only contents/packages permissions. It inherits CI's approved
concurrency policy and leaves existing checks and runner routing intact.
It resolves GitHub's latest published stable release, pulls that
release's existing image by the registry digest recorded in the release notes,
logs that immutable reference and uses the same loaded image ID for both boots.
It boots the image against disposable Postgres 18 with a non-superuser
application role, and seeds a person, project and ticket through
its API. It then applies the candidate's real embedded migrations and backfills,
restarts those same previous-image bytes, and checks health, readiness, embedded
SPA, session/permissions, members, kinds, project summaries, node lists
and seeded node details. An unhealthy startup, failed read or missing seeded row
fails the `migration-compat` job. No image is built or pushed.

Run the same checks locally (Go, Node, Python and Docker required):

```sh
node --test scripts/check-migrations.test.mjs
node scripts/check-migrations.mjs --base-ref v260930115354.0.0
bash scripts/migration-compat.sh v260930115354.0.0 sha256:fceff43ddd6e11473b1fa17669dbf1fc720dfaa4828b3ab2ff2a67a5dbc3aa2d
```

Use the current published release tag and its release-note digest; fetch tags
and their history if absent locally. The job log and step summary record both
the registry digest and loaded image ID as evidence. The script creates and
cleans up only its own containers and network; probe state
lives under ignored `tmp/`. It does not access production or registry writes.
Markus must add `migration-compat` to the required status checks for the main
branch/merge queue to make it a blocking queue gate. PR-time detection is kept,
but two open PRs with the same unused number may each pass in isolation. The
static guard on the combined `merge_group` checkout is the merge-time duplicate
number guarantee once that check is required; queue candidates containing both
files fail. This worker does not change protection settings. Existing required
checks and the AEON-438/459 runner routing are unchanged.
