# Account resets

Reset credits come only from an explicit vendor report on the existing
`POST /api/agent-accounts/{accountId}/readings` path. The registering daemon needs
its existing `account.probe` scope, current `usage_probe` consent and
`binding_revision`. Reports contain `source: vendor`, `count`, one `expires_at`
per credit, `read_at`, `binding_revision` and `undo_supported`. Reports and lists
are bounded to 32 credits. Stale, rebound, private or uncertain reports project
as null in the overview and the daily plan. Expired credits disappear.

The owning person can write `suggest` (default) or `auto_before_expiry` through
`PUT /api/agent-accounts/{accountId}/reset-policy`, using the same `revision` and
`binding_revision` as the floor. Live `account.manage` is rechecked under the
tenant/tree/pairing fence. Opt-in is bound to that owner and binding; relinking
does not inherit it. An identical policy is a no-op.

The plan uses the current window's `account_capacity_learning` burn rate to
wait until exhaustion or five minutes before the earliest credit expiry. It
requires near-exhaustion, fresh readings and an expiry before the natural window
reset. Fresh daemon captures drive automatic use; GETs never spend credits.
After vendor success, reclaimed usage is spread as additional daily percentage
points until the fresh window resets, capped at 50 total daily points. Floors
and explicit Boost today still apply. Running agents are not stopped.

`POST /api/agent-accounts/{accountId}/resets/use` requires `expected_count` and
`binding_revision`. Without vendor Undo, `confirmed: true` represents the inline
confirmation provided by the dial package. Success returns the fresh vendor
window and a durable `action_id`, plus `undo_until` only when the vendor permits
Undo. `POST /api/agent-accounts/{accountId}/resets/{actionId}/undo` verifies that
deadline and binding, then asks the vendor to restore the credit. Successful
operations write `account.reset_used` / `account.reset_undone` audit events;
use includes `account_id`, `expired_at`, `by` (`person` or `auto`) and
`raised_pace_points`.

Vendor execution is an explicit `ResetVendor` capability wired through
`agentaccounts.NewWithResetVendor`. The current native adapters expose no
documented spendable-reset operation, so the default server has no reset
executor and returns `422 reset_unsupported`. No endpoint or credentials are
invented, and local quota never pretends a vendor reset happened. The approved
dial and Settings UI are handled by packages 4 and 5. This backend groundwork
must remain hidden from release notes until a real adapter is integrated.

Before a vendor call, a durable pending action prevents another spend. Calls
are bounded to ten seconds, carry an opaque account identity and action ID for
vendor idempotency, and hold the access fence through their final evidence write.
A timeout, invalid vendor reply or database failure after calling the vendor
returns `502 reset_outcome_unknown`. The pending/unknown record blocks retries,
including automatic retries. Reconciliation requires checking the vendor's
actual outcome; a later ordinary observation cannot authorize a blind retry.
No automatic reconciliation endpoint is included in this package.

Migration 1303 only adds nullable account fields and a tenant-scoped action
table. Rollback retains reports, actions and audit history and runs the older
binary. A down migration must never restore a credit already spent at a vendor.
