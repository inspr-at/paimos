# Account use matrix

Accounts are allowed per work context. Every project is mapped to a context;
project-less work uses the permanent default context. An unmapped project and
the permanent Unassigned holding context fail closed after activation.

The S1 server core provides revision-checked cells, bounded bulk operations,
rules, context lifecycle, project mapping and migration confirmation through
`/api/account-use`, `/api/work-contexts` and the project work-context resource.
Only people with workspace `account.use.manage` can manage these decisions.
Owners and admins hold it by default; agents cannot hold or use it. All saves
re-check current authority under tenant and matrix fences and audit persisted
values. An Undo uses the inverse cells and revision returned by its own save;
a competing change returns 409. Existing running work is listed separately
and continues.

New tenants ask first for accounts and contexts, map new projects to Default,
and allow model versions. Existing tenants keep their former allowance and
Auto-update behavior, with a one-time Needs-you confirmation. Rules apply only
to new resources. Auto-update changes go through the canonical rule writer and
require `account_use_revision`; other refresh settings retain `models.manage`.

The first denial or ledger enrolment activates a monotonic rollback floor.
Database reservation and start triggers enforce context membership; an
unconditional principal-entry guard explicitly refuses older binaries, even
when the account pool is empty. The bound pairing verification run alone is
exempt. Boot checks enforce the database capability floor for future binaries.

The model activation fence consumes a cause on every profile insert, including
disabled pins. Shipped catalogs remain automatic under shipped-only; vendor
successors and older writers are withheld. Deny withholds every automatic pin,
even the first catalog. Withheld pins stay disabled, appear as observations,
and audit the persisted rule and cause at transaction end. Catalog ladders
skip them. Person acceptance, manual additions, line edits and pi model writes
re-check their existing permission; enabling a pin under deny additionally
requires workspace `account.use.manage`.

Settings › Accounts › "Where accounts may work" shows the matrix to people with
`account.use.manage`: the four switches for new accounts, contexts, projects
and model versions; Allow all and Allow none; tri-state row and column boxes;
single cells; Undo (button or U outside text fields) for the last save; and the
context menu for rename, "New accounts: never" and archive. A failed save takes
back what it showed at once, reads the stored state again and says nothing was
saved; when that read fails too, the screen says the state may be out of date.
Rename, "never" and archive carry the revision the menu opened on, so a change
made meanwhile is refused rather than overwritten. A migrated "Only with PAIMOS
updates" model rule keeps its place once left, as a choice that can no longer be
picked. Running work outside the matrix is listed and keeps running. Project
settings choose the project's context and find it on any matrix page; the
account count spans up to 1,000 accounts and says when it stops short. The
pairing review shows the ticks the rule gives new accounts, and model
Auto-update sends the matrix revision as the "New model versions" rule.

Model activation, Go selection filters and daemon enrolment are separate
slices of the same release. This core alone is not a complete release
of the account matrix. Exact-byte migration-policy records are review artifacts
and require the coordinator's review and previous-image compatibility gate.

For the capability sweep, run the whole Go suite with
`AEON_TEST_ACCOUNT_USE_ACTIVATED=1` and the usual disposable test database URL.
The shared fixture template explicitly preserves the previous all-allowed
policy and activates it; policy-specific and pre-expansion migration fixtures
retain their production defaults so their before-activation assertions remain
meaningful. No production predicate, permission or audit is disabled.
The previous-image harness checks the latest published release's reads on the
candidate schema, then boots the pinned release 128 image without
`account_use_v1`. It verifies that image's exact version and non-activated reads
before checking activated empty and populated pools.
Older handlers sanitize database errors, so each refused request must produce
the exact `0A000` message from `aeon_enter_principal` in the disposable
Postgres log. Generic HTTP errors alone cannot pass that gate.

The selection layer uses the same database predicates for dispatch, claims,
explicit account targets, review, catalog choices and account hand-outs. Denied
explicit choices return `409 account_not_allowed_for_context`; queued pinned
work waits with `context`. Catalog, use and capacity-next accept `project_id`;
omission checks Default, while a holding or unmapped project remains denied.

Daily start decisions use a project-filtered copy of the owner's snapshot.
Denied headroom and unknown readings cannot widen that decision. A harness
whose owned accounts are all denied skips to the next resolver ladder step;
a pinned harness and engine admission return `context` without a retry time.
Plan views and stored usage retain all accounts and totals. Context labels are
included in account projections. Unmanaged usage is compliant only with an
explicit allowed account UUID; denied UUIDs emit `account_use.outside_matrix`
and missing UUIDs or label-only reports emit `account_use.unattributed`. Labels never
supply compliance evidence. Pairing verification bypasses context denial only
for the enrollment-bound verification run and account.

CLI account hand-outs accept `--project-id`. Otherwise `project_folders` in
the selected instance maps absolute working folders to project UUIDs; the
longest containing link wins, including through symlinks. Missing links use
Default. Invalid or ambiguous links refuse hand-out, never falling back.
