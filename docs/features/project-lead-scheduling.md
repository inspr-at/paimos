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

GET `/api/projects/{projectId}/lead` also returns read-only `actions` for the
current caller (AEON-1220, `Aeon-Contract: project-lead/1.1`). Each of `start`,
`resume`, `pause`, `adopt`, `cancel_adoption` and `remove` has `available` and a
short `blocked_reason`; the reason is empty when available. The existing
`automatic_launch_enabled` policy is now described in the authored contract.
This backend projection is consumed by the Agents panel integration in
AEON-1212; it does not itself change the panel's controls.

The caller's active canonical person and live project grants determine the
actions. Another person with identical grants receives `lead_owner_required`
for owner actions, including when the intent has no session. Linked identities
of the canonical owner count as that person. All person actions are unavailable
to agents with `person_required`, even with a valid current worker lease.
`harness_control_required` and `run_create_required` explain missing grants;
pause needs only the former permission. Removal retains its existing rule:
any person with harness.control can remove an intent that never started.

Start and Resume also require the effective automatic-launch policy and
executor-confirmed predecessor exit. `automatic_launch_disabled` and
`process_exit_unconfirmed` explain those blocks; lost heartbeat and archive do
not prove exit. Resume additionally needs a paused lead (`lead_not_paused`).
Pause checkpoints and hands over cooperatively, or cancels an unbound start;
`lead_required`, `already_paused` and `adoption_pending` explain its blocks.
An adoption has to select an eligible same-person reporting root coordinator
(`no_eligible_session`), requires an active project (`project_archived`), and
cannot replace a bound lead (`ordinary_succession_required`) or resume a paused
intent (`start_required`). Cancel adoption requires an unclaimed selection
(`unclaimed_adoption_required`). Removal rejects session, generation or retained
generation history (`lead_not_removable`). No ownership-transfer action or
separate managed session Interrupt/Stop permission is implied.

The action view belongs to the surrounding project, revision, generation and
session snapshot. Clients discard it when the signed-in caller changes and
refresh after any lead change; old-server omission or omission on mutation
responses means unavailable until a fresh GET. Reads change no lead, process,
control or audit history and expose no private owner, dispatch binding or proof.
The projection is advisory: every final write still checks current authority,
revision/generation and exact process/lease identity under its existing fences,
and live admission remains necessary before execution.
