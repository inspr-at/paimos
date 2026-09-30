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

Every new migration that drops or renames a table or column must have this
standalone line comment in its header, before SQL (after `aeon:no-transaction`
when applicable):

```sql
-- aeon:contract-phase AEON-415 expanded-in=v260930115354.0.0
```

Replace the ticket and release with the actual contract ticket and the already
published expansion release. Record the expansion/backfill and the old binary's
removed dependency on that ticket. The marker declares the phase; it never
bypasses runtime compatibility checks. Dropping constraints or indexes does not
require this marker. The static guard checks duplicate numbers across the entire
directory, refuses changes to published migrations, and checks new destructive
DDL against this policy (including DDL inside `DO` blocks).

`.github/workflows/migration-compat.yml` runs on pull requests, `merge_group`,
main pushes and manual dispatch, always on GitHub-hosted Linux with read-only
permissions. It resolves GitHub's latest published stable release, pulls that
release's existing image, boots it against disposable Postgres 18 with a
non-superuser application role, and seeds a person, project and ticket through
its API. It then applies the candidate's real embedded migrations and backfills,
restarts those same previous-image bytes, and checks health, readiness, embedded
SPA, session/permissions, members, kinds, project summaries, node lists
and seeded node details. An unhealthy startup, failed read or missing seeded row
fails the `migration-compat` job. No image is built or pushed.

Run the same checks locally (Go, Node, Python and Docker required):

```sh
node --test scripts/check-migrations.test.mjs
node scripts/check-migrations.mjs --base-ref v260930115354.0.0
bash scripts/migration-compat.sh v260930115354.0.0
```

Use the current published release tag; fetch it if it is absent locally. The
script creates and cleans up only its own containers and network; probe state
lives under ignored `tmp/`. It does not access production or registry writes.
Markus must add `migration-compat` to the required status checks for the main
branch/merge queue to make it a blocking queue gate. Existing required checks
and the AEON-438/459 runner routing are unchanged.
