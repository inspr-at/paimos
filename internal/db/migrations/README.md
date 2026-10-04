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

AEON-619 (1138) adds tenant-RLS product settings and per-product project links.
It records the previously selected product as the explicit default, preserves
published products in `legacy` participation, and binds existing browser ballots
to their original product. New products are unpublished with participation
`disabled`; registered activation is refused until B3+B8+B7's reviewed gate.
Legacy portal/pacing tables and response fields remain. The old pace row mirrors
into its bound product link; new writes keep the default's legacy adapter current.
The tenant-loop backfill and mirror delete have an exact-byte policy exception
for coordinator review. DSAR inventory is absent at this base; these new settings
and link identifiers are tenant metadata, while ballot hashes remain pseudonymous
personal data under the existing vote-table contract.

AEON-619 fix round 2 keeps migration 1138 branch-only and updates its exact-byte
exception for coordinator review. Nested and soft-deleted products are backfilled
unpublished with participation disabled. Becoming an eligible root through move,
restore or kind change registers settings idempotently; later moves and restores
preserve the existing slug, default choice and participation settings.

Catalog and participation responses expose `X-Portal-Binding` (`product UUID:settings
revision`) without adding JSON fields. The public page echoes this binding on its
policy read and every wish, correction and vote. Final writes compare it under the
tenant/tree/product fences and return 409 without writing if the displayed product
or settings revision changed. Older clients may omit it. Portal input decoders
run before transaction locks and retain their byte limits, with a 10-second body
read deadline; transactional authorization still runs under the tenant fence.

Fix-round verification: regression-only commit `ccc537dd` keeps the production
code at `090e1f87` and reproduces stale submissions, locked body reads, missing
move registration and dormant backfill. The four new browser regressions also
fail on that baseline. Fixed code at `9128e3b7` passes the remote portal suite,
web build/lint and 1,332 web unit tests. The affected node/database/HTTP and strict
reporter-contract suites pass at `4f31a2d2`; the migration guard and all 24 checker
tests pass. The targeted local portal spec passes all 13 tests with no remaining
owned processes. Remote browsers remain disabled pending OPS-247, so Linux
Chromium and previous-binary runtime compatibility remain coordinator gates.
These are local worker results, not merge, release, deployment or activation
approval. No approved design decision was changed.

AEON-619 lock-order round merged `origin/main` (`d94153bf`) normally at
`1dd63b88`, then replaced branch-added tenant-before-tree fences with main's
canonical helpers. Portal management, moderation, settings and public writes
use `agentpairing.LockMutation` (pairing → tree → tenant); node tree/moderation
entry points use `authz.LockProjectMutation` (tree → tenant, pairing first where
required). Authorization remains inside the final fenced transaction, resource
rows follow the fences, and the event counter remains last. The split portal
locks and redundant intake tree acquisition were removed. Migration 1138 and
its policy exception are unchanged in this round. Generic draft endpoints are
not present in B1; stacked packages consume these shared entry-point fixes.

Barrier regressions at `54df3190` reproduce tenant ownership while waiting for
tree (`55P03` from a separate NOWAIT probe) on 13 node/admin/public paths, and
detect missing tree serialization on two additional paths. Fixed code at
`78e7597e` passes all 17 cases, checking durable mutations and exactly one
matching audit event after the pairing fence resumes. The approved remote
runner passes uncached nodes, portal, authz, agentpairing, agentruns and inbox
suites, including `TestProjectWritesSerializeWithPairingTreeBeforeTenant`.
The existing lock inventory now includes nodes and portal; its scanner and
prior assertions are preserved. Evidence is retained locally in
`tmp/aeon619-lock/negative-remote.log`, `positive-remote.log` and
`static-lock-guard.log`. These are worker checks; consolidated review, push,
merge and release remain coordinator work. No origin push or deployment ran.

AEON-619 final repair round replaces the two node entry-point mutation fences
with main's `authz.LockProjectWrite`. Its tenant SHARE lock excludes authority
writes while permitting tenant FK key-share checks by concurrent comments and
events. Pairing still precedes tree where required, tree precedes tenant, and
authorization remains in the final write transaction. The lock inventory changes
only the two node-helper expectations; shared helpers and their guards stay intact.
No migration, policy exception, API contract or approved design decision changes.

Regression-only commit `3b502ad4` preserves production code from `7b6a6e94`.
Its two real HTTP comment/update and comment/delete cases pause after node SHARE
and tenant-fence acquisition, then use PostgreSQL's wait graph to establish the
edit's node-row wait before resuming the comment's tenant-referencing append.
The approved remote run fails both cases on that baseline (update 409, delete
500 with PostgreSQL `40P01`); the missing durable edit is also asserted.
Repair `a782c0bd` passes both cases in the permitted single-package local check,
including exactly one durable comment and edit audit event per case. Evidence:
`tmp/aeon619-fix3/negative-remote.log` and `positive-targeted-local.log`.

The approved remote runner exits 0 at `a782c0bd` for uncached full nodes, portal,
authz, agentpairing, activity, events, relations and workqueue suites, followed
by the web build, lint and all 1,396 web tests (655 Node tests and 741 Vitest
tests; no Node skips). This includes the earlier pairing/tree barriers and the
lock inventory's negative controls. The regression source is byte-identical to
the failing baseline; shared helpers, three other main guards, contract,
migration 1138 and its policy exception are unchanged. Retained evidence:
`tmp/aeon619-fix3/positive-remote.log` and `preserved-artifacts.json`.

The approved browser runner refuses the four portal specs with exit 3 because
OPS-247's bootstrap and launcher remain pending (`remote-browser.log`). Linux
Chromium verification remains a coordinator gate; no browser gate was bypassed
and no origin push, deployment, model CLI or ticket status change ran. These
results record worker validation, not merge or release approval.

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

AEON-619 merge-main round incorporates release-122 `origin/main` (`068611ab`)
into reviewed branch head `769876fd` with a normal local merge. Both route maps
and README sections are retained. Git-blob checks preserve all 282 main-only
and 31 branch-only changed files; the shared OpenAPI preserves 1,036 path and
component entries from both parents. The automatic migration-checker merge
retains the quota reservation regression and portal exception assertions.
Migration 1138, its exact-byte exception and the canonical lock helpers are
unchanged; the guard confirms 232 unique migration numbers against published
release `v261003095616.0.0`.

Sequential local checks pass for authz, portal, nodes, agentpairing, db, auth,
httpapi and reportercontract, including lock-order and concurrent-comment FK
regressions. Web build/lint, all 1,469 unit tests (686 Node, 783 Vitest), 25
migration-checker tests and all 24 tests across the four portal browser specs
pass. Admin/roadmap initially hit ambiguous locators matching the hidden status
help sheet; only their test locators were scoped, with all assertions retained,
and those two specs pass on rerun. Each browser run leaves zero owned processes.
The first authz attempt preceded creation of its maintenance database; the
subsequent package run passes, and that worker-owned database is now removed.
Evidence: `tmp/aeon619-merge-main/summary.json`, `preservation.json` and retained
logs. Main's release metadata and presentation pin are retained. No feature
changes were authored. mbp2606 was not accessed under the explicit hold; no
ticket status change, push or deployment ran. Consolidated approval and release
remain coordinator work.

AEON-563's `1118_desk_matching.sql` adds an expression index over exact Q&A
fingerprints and nullable per-asker source decision/revision pointers. Existing
question/answer shapes and immutable human revisions remain available to older
binaries. Question writes lock tenant, tree, authenticated key, question and
decision before appending the event; access, key scopes and active decision state
are checked inside that transaction. Matching preserves every membership's
original input, ticket, session and reserved reply root. Reuse increments the
source count once per membership and adds only inbox/comment effects due now;
ask callers identify their new membership by its original request ID.
P3 must use each pending effect's deadline rather than the source answer's human
grace deadline. Corrections retain the original per-asker reuse pointers. Active
Always publication and post-dispatch corrections remain P4/P3 responsibilities.

AEON-503-simple reserves `1213_run_waiting_measurement.sql` for nullable
`agent_runs.waiting_ms`. It sorts after release 122's 1209; 1214 remains free for
this package, while 1215, 1225 and 1230–1239 belong to work-node packages. The
migration preserves legacy run fields, estimate snapshots and outcomes exactly.
No historical timing is reconstructed. The field is personal agent-run timing
telemetry, located by `(tenant_id,id)`; add this classification to the DSAR
inventory when that independently owned inventory lands (absent on this base).

AEON-619 release-123 merge-main round retains latest main `9d81acc6` in normal
local merge `0045620f`, after preserving the inherited pending main merge as
`d0eebb41`. The three conflicts retain both README sections, all exact-byte
migration exceptions and their explicit test expectations. Preservation checks
confirm 296 main-only and 32 branch-only files byte-for-byte, 1,056 OpenAPI
entries, both route maps and 239 unique migration numbers. Main's CI workflow
and shard manifest are unchanged; all four portal specs already have entries.

Local checks pass: 25 migration-checker tests, the migration guard against
`v261003095616.0.0`, OpenAPI reporter-contract tests, strict 12-shard coverage
and all 13 public-portal browser tests with zero remaining owned processes.
The approved remote runner returned exit 3 before code transfer because
Mailina was the console user. Affected-package Go validation remains pending
for the coordinator on the approved remote lane or hosted CI; these local
checks do not claim that validation. Evidence is retained in
`tmp/aeon619-merge-main/summary-r123.json`, `preservation-r123.json` and the
`r123-*` logs. No feature change, origin push, deployment, migration renumbering,
ticket status change or model review ran.
