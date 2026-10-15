# One builder per ticket

A worker generation bound to a ticket occupies its build slot until it is
stopped. Registration, ticket rebinding and lost-contact revival use the same
transactional store guard as queue creation, pickup and attach/watch activation.
Exact registration replay remains idempotent. A second worker receives HTTP 409 with
`code: build_in_flight`, the blocking session ID and public label, who started
it, and when. Coordinator generations do not occupy a worker slot.
An earlier builder remains a blocker after its ticket moves projects. A caller
who cannot see that session receives the refusal with its identifying metadata
withheld; tenant isolation and caller visibility remain intact.
Managed workers' ticket, work-order and run bindings resolve to the originating
ticket for the admission lock, active-worker and delivery checks, and parallel
audit. The session keeps its original binding fields. Conflicting originating
ticket bindings are refused. A standalone order uses an explicit ticket when
one is supplied, otherwise it retains its own build slot.
Attach consent authorizes observation of the process; it does not authorize
parallel work. A refused activation creates no session or lease and retains
the approval for a retry while it is still valid.

When the project has a repository linked through Delivery settings, an open
linked pull request or a projected `work/<slug>` branch with unmerged commits
also blocks a new builder. Repository PR facts match the exact ticket key in a
`work/` branch. The queue explains **already built — ship it** and creates no
work. Merged facts release the delivery block, including when a projection
still describes the older open PR. Projects without a repository link use
the active-worker rule alone. Admission reads stored delivery observations;
it does not perform a GitHub network read or infer local Git state.

With no active worker, registration on the exact pending `work/` branch is a
continuation of that delivery. The repository comes from the ticket's linked
Delivery settings. A new registration or binding returns `build_continuation`
with repository, branch and nullable pull request, and atomically appends
`delivery.continued` plus the ticket comment “continues delivery PR #N on
<branch>” (or “continues delivery on <branch>” for an unmerged branch).
An absent, empty or different branch remains refused. Exact registration
replay emits no further continuation history.

Fix orders can bind `build_delivery: {repository, branch}` at creation. Queue
add (`POST /api/queue`) and parent-snapshot apply accept the same optional
binding. The server validates it, admits the exact pending delivery, and stores
it on the created order. Only a ticket whose own pending delivery the binding
continues stores it: a ticket with nothing pending, such as a clean sibling in
a parent snapshot, is queued without a binding, so its worker keeps its own
branch. Omitting it, or naming another repository or branch, still refuses
with **already built — ship it** and creates no work. Replaying the same
target and stored binding returns the existing queued run; a delivery that
became pending after enqueue refuses the replay instead of attaching silently.
Recurrence enqueue applies the same rule when its caller supplies a binding. A
new occurrence has no pending delivery of its own, so occurrence creation does
not invent one.
The binding is stored in the order's existing node fields and returned with the
order. Queue routing and daemon pickup pass that stored binding to the same
guard; the frozen lead assignment also retains it and rejects a changed binding.
Enqueue does not write the continuation comment. Routing, claim and registration
still record it.
Unknown or different repositories remain refused when a delivery is pending.
An active builder, an unconfirmed earlier writer, stale assignment, revoked
permission or unavailable capacity still prevents continuation. The audit is
flushed after all mutation locks, in the same transaction as the admitted work.

Deliberate parallel work requires a nonempty `parallel_reason` of at most 500
characters. A currently authorized person or coordinator may supply it;
worker authority, a submitted coordinator role or a parent session alone is
insufficient. The reason is stored with the generation and recorded both as
a ticket audit event and a visible ticket comment. Replaying registration
does not duplicate either event. A changed ticket binding needs its own
authorization and reason; a previous ticket's reason is not carried over.
Paused parallel workers retain their same-ticket reason in the continuation
recipe. Starting the successor repeats current person or coordinator authority
and records the new generation on the ticket.

The CLI accepts `--parallel-reason` on `harness register`, `run-heartbeat`
and `run`. The server validates current authority using the initiating key
and live grants inside the fenced write. Existing ownership, lease, exit
confirmation and queue readiness controls continue to apply.

The guard is `workorders.RequireBuildAdmissionTx`, also exposed to queue callers
through `workqueue.RequireBuildAdmissionTx`; its advisory lock is keyed
by the transaction's RLS tenant and ticket. Migration 1343 adds nullable
reason/initiator columns and an index for active ticket workers, preserving
older writers and historical parallel generations. The harness response
contract is `harness-session/3.3`.
