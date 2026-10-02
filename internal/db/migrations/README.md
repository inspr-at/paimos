# Database migrations

`internal/db/migrate.go` applies embedded SQL files in filename order, under a
session advisory lock, and records each completed filename in `schema_migrations`.
Normally each file and its migration record share one transaction.

AEON-452 keeps `1088_more_harnesses.sql` as one numbered migration but commits
its CHECK installation, validation and replacement in three transactions.
Installation uses `NOT VALID` and commits before validation scans, releasing its
exclusive DDL locks. The old enum checks remain enforced until all replacements
validate; only the enum predicates are retired, preserving review null-pairing.
Each phase uses the five-second lock timeout and the migration advisory lock.
Temporary `schema_migrations` phase records include the exact source digest and
commit with their phase, so a stopped run resumes without repeating completed
DDL. Different source bytes on a partial run are refused. Replacement, removal
of only these temporary records and the completed filename record commit
together. Other files retain their existing transaction behavior. The regression
in `more_harnesses_migration_test.go` interrupts after installation, resumes
validation with a writer lock held, and verifies replay and ledger cleanup.

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

AEON-486 backfills `models.read` into existing custom roles bound to agents in
`1061_agent_roles_models_read.sql`. The migration runner enters each tenant on
the advisory-lock connection before applying this additive SQL, because FORCE
RLS also applies to the migration owner. Each tenant commits independently;
`ON CONFLICT DO NOTHING` makes a partially completed run safe to retry. The
filename is recorded only after every tenant succeeds. Key scopes are unchanged.

AEON-502 A (1104–1107) adds work kinds, sparse model preferences, run
residency/starter stamps, session placement and profile retirements. An empty
matrix preserves existing role ladders. Model locks bound Default → You →
Project selection; residency locks allow any narrower choice and flag loosening.
Ticket requirements and existing run stamps remain floors. Account routing
recomputes the canonical starter's live requirement before every reservation,
recheck and vendor retry; no account evidence means class `any` until AEON-473.
Pairing verification remains exempt. Preference endpoints, planning callers
and UI editing follow in 502b–e.

Migration 1104's tenant-loop seed and replacement area-schema helper require an
exact-byte exception in `scripts/migration-policy-exceptions.json`. The new
slug pattern includes every previous area, and current Go area validation stays
unchanged. Coordinator review and previous-binary compatibility CI remain
required before merge/release. Before rolling back below this fence, set all
residency requirements to `any` or pause dispatch; older binaries cannot enforce
these stamps.

AEON-502 C adds `1130_tenant_work_kinds.sql`, an additive tenant-insert trigger using the existing idempotent work-kind seed. It covers startup `EnsureTenant` inserts under forced RLS and restores the caller's tenant setting. Existing tenants retain their configured kinds; no table, column or historical migration changes.

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

AEON-475 adds `aeon_node_eta_progress` for completion-aware epic roll-up and
`aeon_node_completion` for the latest worker's completion evidence. New node
reads and progress sorting use these functions together; a cleanly finished
child contributes 100 without retaining its stopped session's ETA. Nested epics
still count as one direct child, and unknown or failed work stays unknown.
The published `aeon_node_eta` function remains available to previous binaries.

AEON-523's `1080_invited_alias_access.sql` adds `aeon_bind_legacy_uninvited`,
used by sign-in, import and unlink to preserve an accepted invite's explicit
access. Linked aliases never seed access, and people with aliases retain their
explicit bindings without recreating grants from legacy labels. The original legacy binder stays
available for previous binaries; no published SQL or function is replaced.

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

The allowlist covers `CREATE TABLE`, `CREATE INDEX` (including `UNIQUE` and `CONCURRENTLY`),
`CREATE TYPE`, `CREATE FUNCTION`, `CREATE POLICY`, `CREATE TRIGGER`, nullable
`ADD COLUMN` or one with a constant non-null default, `ADD CONSTRAINT … NOT
VALID`, `VALIDATE CONSTRAINT`, `COMMENT ON`, `GRANT`, `INSERT INTO` / `UPDATE`
backfills and `SET LOCAL`. Tables created without `IF NOT EXISTS` may enable and
force tenant RLS in the same file. A nullable column's default must also be constant; function calls
and expressions are not accepted as constant defaults. Replacing a function,
dropping constraints/defaults/policies/indexes, changing types, setting an
existing column `NOT NULL`, renaming, truncating and deleting require a marker.
All `DO` blocks and dynamic `EXECUTE` function bodies require a marker, including
SQL assigned to a variable before execution. The checker first splits SQL with
an exact port of the runner in `internal/db/sqlsplit.go`, including its first-`*/`
block-comment termination, comment removal and quote handling. A parity test
compares their statement lists over comment/quote edge cases and every embedded
migration; CI runs it explicitly with Node installed. The tokenizer keeps
quoted identifiers and string/dollar literals separate from executable SQL.
Every statement must be allowlisted; unknown forms fail closed.

A bare validated foreign key is also allowed when the same `ALTER TABLE` adds
at least one of its referencing columns with the built-in `uuid` type, no
default and no constraints. Every existing row has NULL in that new column, and PostgreSQL's
default `MATCH SIMPLE` exempts the entire key. This narrow form excludes
`IF NOT EXISTS`, a foreign key on existing columns alone, separate statements,
domain types, `MATCH FULL`, referential-action suffixes and any other unsafe
ALTER action.

Merged but unreleased contract steps must not be disguised as expansion or
added to historical grandfathering. `scripts/migration-policy-exceptions.json`
is a separate, explicit review artifact naming each file, its SHA-256, owning
ticket and rollout reason. The guard prints every exception, rejects malformed
or duplicate entries and changed/removed files, and retains number uniqueness
and published/baseline immutability checks. An exception does not make its SQL
expand-safe and does not skip runtime compatibility. Coordinator review of
each exception is required before merge/release; workers only measure draft CI.

The sole initial exception, AEON-397's `1054_confirmed_quota_pools.sql`, replaces
the reservation guard immediately. Existing accounts start unconfirmed, so
self-reported fingerprints cease to authorize shared-pool reservations until
a person confirms the pool. Existing active holds can release/settle. The
previous binary's read probes do not test these writes. The owner must accept
that intentional safety contract change before release, or commission a
follow-up migration that stages a compatible guard until a later release;
never infer consent by backfilling confirmation from self-reported hints.

`scripts/migration-policy-baseline.json` explicitly grandfathers a set of
filenames, each pinned by exact SHA-256 content from the published release and
the main commit this policy started from. These source commits are recorded,
and a regression test verifies the complete manifest against their Git trees.
Changing or removing a pinned file fails, so adoption preserves historical SQL.
The current published release's exact filenames are also immutable. The static
guard still classifies all files and checks duplicate numbers across the entire
directory. Every other filename must meet the allowlist or supply verified
contract evidence, even when its number fills a gap below the latest release.

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
GOMAXPROCS=2 go test -p 2 ./internal/db -run '^TestMigrationCheckerSplitParity$' -count=1
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

AEON-483 uses three branch-only migrations: 1100 adds a nullable content address,
installs the replacement byte check `NOT VALID`, creates digest-only alias receipts
and durable chunk tables, and drops the old byte check in an isolated statement.
That drop is its only policy exception. 1101 validates the check in a separate
transaction after 1100 releases its exclusive lock; 1102 builds the unique content
index with the first-line `aeon:no-transaction` marker. The runner accepts exactly
one concurrent index statement per marked file, including unique indexes, and
checks uniqueness before reusing a valid unrecorded index.
