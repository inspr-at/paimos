# Project lead scheduling (AEON-739 backend groundwork)

Explicit project leads consume the existing person's agent dial, including
idle leads and generations with unconfirmed exit. The server supplies no extra
lead slots. Queue is the only scheduling input; it never grants execution rights.
A successful new route records one project turn. The next turn orders qualified
project demand by the later of its oldest eligible queue arrival and its last
successful route. This gives waiting projects a turn while preserving targeted
and manual ticket order inside each project. Idempotent routing does not advance
turns. Revoked owners, archived projects, unresolved dependencies, stale reporting
and unavailable live lead gates cannot win a competing turn. Competing demand
also needs a usable worker model/account route, live claim permission and no
pending worker assignment. Scheduling reads are owner-scoped, limited to 200
entries with a three-second deadline and 64 KiB per ticket's fields; excess
demand returns an explicit wait error. Lead pickup also caps its project queue
before fields are decoded or entries projected.

A worker uses `aeon lead yield --project KEY --expected-revision N --generation G
--worker-lease-file PATH` (or `-` for stdin) when idle, waiting for an accepted
worker, or yielding its project turn. The server validates that condition and
reuses cooperative checkpoint/pause. The process keeps its slot until confirmed
stopped. Only a worker-priority yield with a retained checkpoint allows previously
routed assignments of the exact current generation to start afterwards. Person
pauses and replacement generations fence them. Every final worker start still
checks dial, harness, account room and host load. Ready routed workers have
priority over lead restart; restart remains an explicit intent through ordinary
admission. No workers are adopted or reassigned. The production admission adapter
and automatic launch remain disabled until AEON-603 end-to-end qualification.

Lead snapshots expose `automatic_launch_enabled: false`, independently of
admission adapters. Start and Resume stay unavailable with this explanation;
an unreadable or older snapshot also keeps them unavailable. An existing start
intent stays cancellable and says it is waiting for a runtime while automatic
launch is off. New intents store `awaiting_generation`; the old first-insert
`start_checks_unavailable` reason is corrected on reads, without changing stored
history. An actual failed admission names the check when identified; an absent
or failed adapter reports `admission_unavailable` without exposing error text.
The wait projection is bounded to five minutes for a future qualified enabled
launch, then reports `runtime_pickup_timeout`. This only changes visible state:
it does not launch, retry, cancel, free slots or change Engine W2 dispatch.
