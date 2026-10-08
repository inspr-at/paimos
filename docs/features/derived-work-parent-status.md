# Derived work-parent status (AEON-650)

Migration `1225_work_parent_status.sql` installs the shared transaction engine.
`db.InTenant` enters pairing → tree → tenant → sorted work rows before a writer;
`db.InTransaction` coalesces nested writes before the outer commit. Both old and
new ancestor chains participate in moves. Canonical reads temporarily raise
project visibility only inside the SQL functions, restoring it on every return;
normal authorization remains in each writer's final transaction. Derived parent
updates preserve `updated_at`, so they do not invalidate an unrelated title/body
edit. Live views consume `status_autopilot.derived` at that unchanged revision.
At that revision, the row store rejects node responses sent before a newer
accepted read or derived hint. Late list responses preserve status from newer
node reads while still supplying list-only projections. List snapshots retain
their server-position ordering; cross-source status uses request order when
positions cannot be compared. Status and its ordering fence form one snapshot:
an accepted node payload applies before its fence advances, including when a
full cache was already confirmed. A post-gap read applies the recovered status
before confirming freshness. Named regressions and 300 reproducible seeded
node/list/stream-gap interleavings check this against a small reference model.

Rollout remains OFF until AEON-429's `features` table and service merge. Its
catalog must register `work-parent-status` (label: “Parents follow their work”),
using its existing tenant/project inheritance, explicit project OFF and default
OFF behavior. Before enabling it, provision the existing System actor in a
separate committed transaction while the flag is OFF (`systemactor.Ensure` /
`aeon_authz_system_actor`); activation without that actor fails closed.
Activation plumbing must validate the row/depth bounds before saving ON. A tenant
already above the live-row limit can still read and disable the flag; flagged
work mutations fail closed until recovery. This checkout does not substitute a
second flag API. The coordinator owns catalog
registration, activation plumbing and the AEON-429 integration check. Existing
parents reconcile when their children or state-category configuration change;
activation itself does not rewrite historical states. Package AEON-655 owns the
parent status controls and preview-confirmation UI.

Live work children make a parent; other kinds do not. Doing/QA wins, then
Blocked, then Open. With only finished children, any Done gives Done; only
Cancelled gives Cancelled; all Accepted gives Accepted; Delivered/Accepted gives
Delivered; other finished mixtures give Done. Custom categories use these same
buckets. Archived children are ignored. No remaining active work children keeps
the last state and records a retention reason, including when the last work
child converts to another kind. PATCH rejects an explicit parent
status with `409 parent_status_derived`, including the current value; bulk reports
such parents as skipped while applying authorized leaf changes. Imports retain
canonical parent status and report `parent_status_derived` as a source conflict.
Requirements generation and release quick-create emit work nodes.

History and SSE expose safe `derivation` context: rule version, generic reason,
affected parent ID, and cause event ID only when that event is visible. No hidden
child identity or count is included. The private audit metadata retains
selected cause IDs for grouped writes; ambiguous causes offer no causal Undo.
Derived changes use Status autopilot's audit
surface. The existing single-click Undo stays unavailable for them. Fetch
`GET /api/events/{id}/undo-preview`; confirm its visible `change_type`, message
and affected children, then POST `/api/events/{id}/undo` with
`{"confirmed_cause_event_id":123}` using the returned cause ID. The write
decodes its bounded confirmation before taking any transaction locks, then
re-checks permissions and original child revisions under access-change fences
even if the flag was disabled, and reverses the cause once. Parents are derived
again while the feature is enabled. Ordinary Undo also takes pairing, tree and
tenant access fences before its event fence in both flag states, including
across activation and concurrent knowledge writes.
Later child edits, hidden/ambiguous causes, active work and unsupported reverse
operations refuse the action atomically.
Causal preview and confirmed Undo enforce the leaf’s bilingual benefits when restoring
completion, using current tenant state categories, including custom Done.

The initial implementation deliberately serializes flagged tenant transactions
and prelocks their work rows to avoid taking ancestor locks after the event
counter. Reads also enter that protocol within capacity. Limits fail the entire
work mutation transaction: 50,000 live work rows (tombstones are excluded, and
restoration counts toward the limit), 1,000 changed nodes, depth 1,000, 10,000
cause events, and 10-second entry/derivation deadlines. Causal previews accept
at most 200 children and 4 MiB of combined cause snapshots, checked on their
expanded JSON in Postgres before transfer to the application. Confirmation
bodies are limited to 1 KiB with a 10-second HTTP read deadline. Shared work-order,
harness, run, hours and review endpoints, queue writes and brand settings buffer
their existing bounded request bodies before transaction admission, with the
same 10-second HTTP read deadline. Event-position buffering preserves that
connection deadline on harness and run GET routes without flushing responses
before their position is known. The live-row limit is
checked again against the final tree before commit, so crossing creates,
restores and kind conversions roll back atomically. Direct work writes outside
`db.InTenant` fail closed when the flag is enabled. These bounds and tenant-wide read serialization require
load validation before enabling large tenants; the local deep/wide regression
covers 64 ancestor levels and 300 leaves, not a production load claim.

No version, release pin, session/Decision Desk identity or historical event is
rewritten by this package. AEON-649's backup-only kind migration rollback still
applies. No permanent tables or columns are added; only transaction-local queues,
SQL functions/triggers, and existing append-only event metadata are used.
