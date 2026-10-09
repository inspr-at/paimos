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

Matrix UI, all Go selection filters, model activation and daemon enrolment are
separate slices of the same release. This core alone is not a complete release
of the account matrix. Exact-byte migration-policy records are review artifacts
and require the coordinator's review and previous-image compatibility gate.

For the capability sweep, run the whole Go suite with
`AEON_TEST_ACCOUNT_USE_ACTIVATED=1` and the usual disposable test database URL.
The shared fixture template explicitly preserves the previous all-allowed
policy and activates it; policy-specific and pre-expansion migration fixtures
retain their production defaults so their before-activation assertions remain
meaningful. No production predicate, permission or audit is disabled.
The previous-image harness also checks activated empty and populated pools.
Older handlers sanitize database errors, so each refused request must produce
the exact `0A000` message from `aeon_enter_principal` in the disposable
Postgres log. Generic HTTP errors alone cannot pass that gate.
