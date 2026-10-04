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
recheck and vendor retry; no account evidence means class `any`.
Pairing verification remains exempt. Preference endpoints, planning callers
and UI editing follow in 502b–e.

AEON-604 (1129) stores host-owned residency evidence per account in a tenant-RLS
table. The record includes bounded country sets, profile coverage, local
execution, verification/expiry, proof reference and optional retention/training
declarations. Writes snapshot the account binding and emit an event; the fence
rejects missing, expired, future-dated or superseded evidence. The additive table
does not change existing account rows or grant any account residency by default.

Migration 1104's tenant-loop seed and replacement area-schema helper require an
exact-byte exception in `scripts/migration-policy-exceptions.json`. The new
slug pattern includes every previous area, and current Go area validation stays
unchanged. Coordinator review and previous-binary compatibility CI remain
required before merge/release. Before rolling back below this fence, set all
residency requirements to `any` or pause dispatch; older binaries cannot enforce
these stamps.

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

AEON-613 fix5 reserves **1147** for quota warning settings and durable receipts,
replacing unpublished migration 1136. The current shared ledger assigns
1136/1137 to AEON-615 and reserves the whole 1139–1146 block for other tickets.
The replacement reservations are recorded on AEON-613 before renaming; the
coordinator must mirror 1147/1148 to the shared ledger because workers may
author only inside their own repository. The settings/receipt SQL is unchanged.

DSAR integration (AEON-490): this branch has no `internal/dsar/inventory.json`.
Classify `quota_warning_settings` as metadata, located by `(tenant_id)`.
Classify `account_quota_warnings` as personal quota telemetry (resource/window
identity, reading time, remaining percentage, reset, threshold and recovery),
located by `(tenant_id,id)`, when merging with the inventory branch. These
records contain no credentials, local paths or raw vendor responses.

AEON-613 fix5 reserves **1148** for `account_quota_warning_observations`,
replacing unpublished migration 1137. This stores the latest measured
quota/window state, including healthy readings, separately from notification
receipts in 1147. Only its filename and receipt-migration comment change.
The reservation is recorded on AEON-613; the coordinator must mirror it to the
shared ledger under the same ownership boundary. Existing receipts remain
unchanged, and current availability resumes with the next fresh measurement.
The first fenced write seeds its watermark from retained receipt reading and
recovery times. A historical recovery with no retained healthy percentage stays
unknown; it never synthesizes a quota figure. A newer measured observation
replaces it, while delayed readings remain blocked across reset transitions.
Classify this table as personal quota telemetry, located by
`(tenant_id,quota_key,window_key)`, when integrating the DSAR inventory.

AEON-649 reserves **1215** from its coordinator-assigned 1215–1224 range for
`1215_one_work_kind.sql`. Its same-transaction runner hook reconciles legacy
work schemas under FORCE RLS, validates existing fields against the merged
schema, rewrites allowed-child references and substitutes the work kind on
live and deleted epic/ticket/task nodes. Keys, IDs, parent links, timestamps,
status, fields, estimates and release-note settings stay unchanged. Historical
sessions, Decision Desk nodes/projections and journey memberships retain their
IDs and references. One append-only `node.work_kind_migrated` snapshot per
changed node records the old kind and the backup-only rollback policy.

Property definitions and state categories must agree wherever they overlap;
required sets must agree across kinds. Incompatible constraints, a pre-existing
reserved `work` slug, invalid historical fields or fields over the bounded
1 MiB validation limit stop the entire migration with tenant/node identifiers,
without exposing field values. Reconcile explicitly, then retry. A live work
parent with a bound session, queued/active claim or running child work order
also stops the migration and reports up to 100 keys. Only live work children
make a parent; stopped historical sessions and non-work children do not block.

This is a maintenance migration, not an expand-safe upgrade. The exact-byte
policy exception is a coordinator review artifact. Release 122 must be
published and the merge/work queues drained; stop writers, verify an
instance-specific pre-upgrade database/files backup by restoring it elsewhere,
and deploy the completed work-node chain before AEON-596 ship adoption.
The worker does not approve, push or deploy it. New naming and derived status
remain downstream features behind AEON-429; this package changes no UI.

The migration takes pairing → tree → tenant → resource locks before publishing
any events. The tenant table fence prevents concurrent seeding, and maintenance
table locks freeze sessions, runs and work orders. The tree validator is disabled
only while substituting kind IDs, so tombstones under deleted parents migrate;
it is restored before event publication in the same transaction, including on
rollback. Other validators, RLS and the append-only event guard remain active.
No resource mutation or resource lock follows the final event-counter pass.

Local drill: with `aeon-dev-db` running, execute the targeted
`TestWorkNodesVerifiedBackupRestoreDrill` test in `internal/db`, using the local
`AEON_TEST_DATABASE_URL`. It creates synthetic disposable databases, retains a
custom-format archive and SHA-256 evidence at `tmp/aeon-649-wn/`, compares every
public table plus starter/tree-function digest, and migrates the restored old
schema with an ordinary non-bypass role. The already-installed pgvector
extension is excluded from the archive and supplied by the recovery bootstrap.
This proves the local fixture round trip, not a production restore. Rollback
restores the matching old database/files backup and exact old binary; per-node
Undo and running an old binary against the migrated database are unsupported.
Importer replay keeps `fields.classic.type` and normalizes only the exact recorded
kind substitution, so person edits still conflict. Existing classic journey
ticket membership semantics use retained provenance, including stored import
events during relation backfill; broader parent/leaf release placement belongs
to AEON-652. No permanent tables or columns are added for DSAR classification.

## AEON-650 reservation and activation

**1225** is the engine migration in AEON-650's coordinator-reserved **1225–1229**
range. The coordinator must append `1225 AEON-650 (work parent status engine)`
to its shared ledger; the worker does not author outside its worktree. Slots
1226–1229 remain unused. No permanent user-data tables or columns are added.

The engine defaults OFF with no AEON-429 table/override and honors tenant ON,
project OFF, and null inheritance using the exact AEON-429 storage contract.
Register `work-parent-status` in that package's catalog and provision System
before activation, in a committed transaction while the flag is OFF. It refuses
missing actors rather than acquiring a principal-link lock after an event
counter. Every flagged work write must enter `db.InTenant`; grouped operations
use `db.InTransaction`. SQL guards cover imports, requirements, quick-create,
bulk, move, delete, restore, Undo and category updates. Changed children schedule
both ancestor chains; final derivation reads locked canonical rows and emits at
most one transition per parent. Retention has a separate audit event.

Capacity counts live work rows and checks the final tree before commit; crossing
creates, restores or kind conversions roll back. Tombstones do not consume the
live-row limit. If an already enabled tenant exceeds it, entry leaves derivation
inactive and restores caller visibility, allowing reads and feature disablement
while the work-write guard fails closed. Causal Undo independently fences access
changes when the flag is disabled, and reads confirmation before transaction
entry. Ordinary Undo also takes its tree fence before its event fence across
activation. Cause snapshots are bounded on expanded JSON size in Postgres before
transfer. Kind conversion away from the last work child emits retention evidence
with the conversion cause. Cause references stay in private metadata and are
exposed only through the visibility-checked event envelope; legacy snapshots
are redacted on read.

The migration does not backfill statuses or enable flags. Initial reconciliation
is caused by child changes or state-category configuration changes. Before any
rollout, the coordinator validates the AEON-429 integration and whole chain,
including the parent UI, under AEON-649's maintenance/verified-backup gate. See
the root README for the API contract, explicit bounds and read-serialization
tradeoff. This worker neither pushes nor deploys.


## AEON-653 release placement and recurring leaves

Coordinator-reserved **1233–1234** belong only to AEON-653. The coordinator
records their individual purposes in its shared ledger; workers author only in
their own worktree. Migration 1233 adds `work_parent_releases`: tenant, parent,
project and release UUIDs are metadata, located by `(tenant_id,parent_node_id)`.
The DSAR inventory does not exist in this stack or its local origin/main;
classify these four columns and 1237's retained membership projection when
integrating AEON-490's inventory. The retained object stores journey UUIDs,
source, position, estimate and scope/access flags as metadata at the same locator.

`ticket_node_ids` accepts parents and leaves. Placement expands work edges in
the same project and deduplicates overlapping roots, with at most 100 roots,
1000 resulting leaves and a separate 50,000-node traversal bound; intermediate
parents do not count against the leaf limit. Excess rolls back. Parent
intent is separate from release membership. Future leaves inherit the nearest
placed ancestor's release only while it is planning. Otherwise they remain
unassigned, become Backlog and carry `release_inheritance_note=parent_release_closed`.
A fresh inherited leaf advances project/release revisions, invalidating stale
plans and Undo. Undo restores both leaf membership and parent intent, only while
its fenced result is current. Membership reads report actual distinct leaf
releases, not intent, and return bounded leaf identities for honest client counts.
Scopes stop at non-work children and nested projects; caller RLS stays active.

Migration 1234 normalizes stored recurrence template references to work and
increments definition revisions without rewriting occurrence receipts or prior
sessions. REST keeps epic/ticket/task template aliases. Each occurrence creates
a work leaf; the existing AEON-650 engine reopens its parent when
`work-parent-status` is enabled through AEON-429. Parent Done is never a
recurrence trigger. Historical sessions and Decision Desk IDs are untouched.

Migration 1237 (AEON-653 fix2) reconciles current planning membership on work
creation, moves, restoration and deletion. A leaf becoming a parent keeps its
placement and the complete bounded membership projection as intent and leaves
the live member set. Returning to leaf restores feature, position, source,
estimate and scope/access flags; Undo follows the same reconciliation path.
Explicit membership changes synchronize retained intent, including plan
removal to backlog and compensating Undo, so subsequent children follow the
latest choice. Changing parent placement invalidates its retained scope approval,
including an ordinary move back to the original release. Membership events capture that
flag so compensating Undo can restore the prior approval; restoration also
requires fresh scope review when older retained metadata names another release.
Other retained planning metadata stays intact. Released/frozen member rows
and stored note snapshots remain unchanged. Live walkers, current-release journey
calculations and new note captures exclude parents. Quick-create applies explicit inclusion
even after automatic inheritance and advances each revision once. Membership
reads additionally return `assigned_leaf_count`; clients compare it with current
leaf identities before confirming that a replay placed the entire subtree.

Integration seams: the existing release-note capture helper and manifest
backfill accept work leaves (AEON-596 P3/P6); immutable published snapshots stay
unchanged. System recurrence queue entry explicitly checks leaf shape before
using the legacy readiness helper (AEON-652), pending its consolidated lifecycle
changes. Review both seams when merging siblings. Neither migration enables the
rollout flag. Whole-chain acceptance, verified backup/rollback, push and deployment
remain the coordinator's gates; this worker performs local checks and commits.
