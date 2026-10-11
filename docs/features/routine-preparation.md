# Routine work preparation

The internal `agentruns.PrepareRoutineWorkTx` seam prepares only existing queued
leaves linked to a pending routine run. The current definition revision, frozen
execute consent, execution identity, output project/parent and occurrence marker
must agree. Current person consent and exact-artifact project qualification are
checked again under the shared tenant/tree fence. Execution remains off by
default; this slice registers no scheduler, endpoint or process launcher.

Preparation uses the existing targeted, priority/FIFO and manual queue order.
It never adds arbitrary backlog work or changes release membership. Live
blocking relations are inspected through a bounded redacted probe: unresolved
sources wait, and sources outside caller visibility remain unavailable. A
blocked status, active person pickup or unconfirmed previous writer is ineligible.

With accepted criteria, a missing estimate produces a suggestion for the current
typed queue estimate action. The suggestion carries the node revision and exact
accepted criteria digest. Preparation does not apply the action: the executor
must retain its normal budget, guardrail and action gates. New or changed
criteria produce a person draft bound to the same node, revision and previous
criteria digest. Creating, rejecting or replaying a draft performs no write and
preserves the accepted criteria. Person acceptance requires the existing typed
person action, followed by preparation of the resulting revision.

One step permits at most 32 candidates, 64 KiB of title/body/fields per candidate,
64 blocker relations per candidate and five seconds of local analysis. Candidate,
field and blocker limits return explicit partial reasons with no usable
candidate. A deadline returns `preparation_time_limit`, `partial: true` and an
error; the caller rolls back rather than consuming the scan as complete. There
are no provider/model/network calls or provider charges in this step.

Before a later typed write, `RequireRoutinePreparationTx` rechecks consent,
ordering, readiness, node revision and criteria identity under the same fence as
human pickup. The caller enters this guard before resource/event locks, retains
the transaction through its mutation, and uses the shared queue admission. The
guard grants no launch or criteria acceptance and creates no second assignment.
