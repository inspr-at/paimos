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
contract is `harness-session/3.1`.
