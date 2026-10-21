<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Goals API

AEON-1456 adds project and workspace goals, with outcomes stored as ordinary
child nodes. Existing release nodes can also be read and updated as goals;
release membership and release publication are separate concerns. No sample
data is created. The tenant goal/outcome kinds are restored lazily on an
authorized create or fresh update, including a release's first outcome.

The contract is authored in `api/areas/goals.yaml` and served with
`Aeon-Contract: goals/1.5`. It provides:

- `POST /api/goals` for a goal title and optional project UUID.
- `PATCH /api/goals/{key-or-id}` for ETA, steps, a flagged progress override,
  and a bounded batch of outcome upserts, removals, moves and handover changes.
- `GET /api/goals` and `GET /api/goals/{key-or-id}` for filtered state, progress,
  estimate coverage, effective ETA, passed, project badges, questions and stall.
- `GET /api/goals/{key-or-id}/history` for descending event history. An
  `outcome` UUID keeps the same timeline after a move.
- `GET /api/goals/{key-or-id}/series?range=24h|7d|30d` for recorded progress.
- `GET` and `PATCH /api/goals/status-note` for the workspace status note
  (AEON-1494).

Writes require `nodes.write` in each affected goal's current scope. An agent
key also needs `goals.read` or `goals.write` as an outer key-scope ceiling. A
person grants that scope on the existing key-scope path; the grant is audited
as `agent_key.scopes_changed`, and new keys do not receive either scope.
Viewer and guest roles do not receive `goals.read`. Handlers still recheck
`nodes.read` or `nodes.write`. A required
`Idempotency-Key` binds the tenant, actor, method, immutable target and canonical
body. Replays recheck current authority and retain their original response,
`read_through`, `generation` and `changed`. Another target or body conflicts. Relative ETA clock bounds apply to fresh
receipts; aging past that bound does not invalidate a previously committed replay.
New keys for unchanged data produce no domain event. Request bodies are bounded
to 256 KiB and a read deadline; validation and mutations are atomic.

Every outcome upsert requires a stable client `ref`; removed or moved-out refs
cannot be reused in the source goal. An outcome serves at most 20 visible node
keys/UUIDs through relations. Only work-kind targets contribute work leaves.
The bounds are 200 live outcomes, 200 operations, 200 retired refs and 20 steps.
A whole-list replace needs the latest goal event ID in `if_read_through` and
complete visibility of the current list. Explicit edits can still update a
visible outcome when another sibling is hidden. A second handover conflicts
until the existing marker is explicitly cleared.

An outcome contributes only when the goal and every serves end are visible.
Hidden outcomes contribute no values, counts, projects, history or stall changes.
An active eligible worker takes precedence over explicit state and handover;
otherwise non-empty all-done leaves derive Done. Progress shares estimated
hours across overlapping outcomes. Partial coverage contributes known hours
only and reports `basis: mixed`; wholly unestimated outcomes receive the mean
known weight, or one when there are no estimates. A manual override remains
flagged and retains its author, time and note.

Authored ETA takes precedence over worker fallback. Source writes record
outcome ETA transitions in the same transaction, including heartbeat and direct
coordinator reports. Goal-level fallback notifications carry only a change ping.
The read computes passed when an unfinished value is more than ten minutes old;
a scheduled bounded pass records flips without sampling ETA history. Neither
reporting cadence nor the job gates the read's passed bit.

All responses carry a visible event watermark and a monotonic generation.
Consumers compare `(read_through, generation)` and discard an older response,
including when time-driven reads finish in reverse order. Subscriptions accept
`topics=goals` and apply current outcome/reference visibility as well as the
existing resource permission checks.

Progress series reconstruct outcome membership, recorded node state/estimates,
parent changes, worker activity and overrides from visible events. They return
24 hourly, 28 six-hour or 30 daily UTC points. Each bucket carries `t`, `pct`,
`delta` versus the preceding bucket, and visibility-filtered `changed_keys`.
State changes remain listed when the percentage stays the same. `keys` is
retained as a compatibility alias; `keys_truncated` reports the 200-key bound.
The projection is bounded to 200 points, 4096 roots, 50,000 scope pairs, 20,000
events, 32 MiB of event snapshots and a work budget. An oversized detail or
series returns HTTP 200 with `expansion: too_large`, without partial derived
values. Detail preserves visible outcome/target counts. Stall requires a visible
Moving outcome and an unchanged displayed percentage for at least four hours;
`since` is the recorded last percentage change, or goal creation.

List filters include the goal's home and visible contributing projects/groups.
Search and state filters do not alter the full-filter totals. Pagination uses a
filter-bound cursor and stable tie-breaking. Up to 100 goals share one materialized expansion query. The state and shared
ETA projections reuse one invoker work scope; historical facts supply stall.
Full-filter totals and computed sorting use the same bounded batches across at
most 1000 matching goals. Larger sets require narrower project/group filters.
The aggregate limit is 4096 roots and 50,000 root/node pairs, with separate
bounded historical discovery. A page that exceeds a combined budget shrinks,
returns `truncated: true`, and provides `next_cursor` for the first omitted goal.
A goal that exceeds the budget on its own remains in the result with visible
counts and `expansion: too_large`; its siblings still expand. `totals_partial`
identifies oversized goals omitted from state totals. Decoded batch data and
retained list snapshots each have a 32 MiB bound.

Desk asks accept `goal_id`, and `aeon ask --goal KEY` resolves it. The association
requires current `nodes.read`, is stored as an `asks_for` relation per ask, and
survives equivalent questions merging. Question reads recheck the associated
goal's visibility and key ceiling.

The CLI exposes `goal create|update|list|get|history|series`. Create and update
require an explicit stable `--idempotency-key`. Update accepts bounded JSON via
`--file PATH|-`, or `--eta`, `--eta-at` and `--progress N|clear`; `--dry-run`
validates structural bounds and prints the request. The server checks relative
ETA clock bounds for a fresh receipt. The UI belongs to AEON-1457.

## Status note, item notes and the In-flight read model (AEON-1494)

The workspace status note is one row per tenant (`goal_status_notes`, migration
1362, tenant RLS): text (1–2000 characters), author and time. `PATCH
/api/goals/status-note` takes `{"text": "…"}` or `{"text": null}` and the usual
`Idempotency-Key`; it requires `nodes.write` in the workspace, rechecked under the
tenant and tree fences before receipt replay. Repeating the current text changes
nothing. Each change appends `goal.status_note_changed` (no node; before/after
snapshots), which `topics=goals` subscribers and the event stream receive when they
read the whole workspace. Reads need workspace `nodes.read`; `GET /api/goals`
carries the note as `status_note` only for such readers.

An outcome's `note` now records `note_by` and `note_at` when its text changes;
reads add `note_by_name`. Null or blank text clears all three. Reads also add, per
done item, `merged_at` (the latest `merged_at` a merge recorded on its done work
leaves); per goal, `progress.remaining_hours` and `progress.remaining_unestimated`
(the open leaves of unfinished items, each once); and on the list
`eta_last_set_at`, the latest authored ETA over every goal and item the project/group
filters match. The CLI adds `goal note --workspace [TEXT|--clear]` and
`goal note GOAL --item REF (TEXT|--clear)`.

## Group › Epics (AEON-1395)

The Goals view on Projects has a Display › **Group: Goals · Epics** segment. The
choice is kept per person in the `goals` preference; `?view=goals&group=epics`
opens Epics for that visit without changing it. A switch replaces the rows and
keeps the toolbar, state chips, Display trigger, workspace status note and
column header (the GOAL and EPIC labels share one box, so no header cell moves).
The empty state waits until the new list has arrived.

Epics come from `GET /api/goals/epics`, which takes the Goals list filters, sorts,
page and cursor (cursors are bound to their grouping) and returns open epics in
the goal shape with `kind: epic`. The parameter `group` keeps its meaning as the
project-group filter. An area is the topmost open parent work item in a visible
project: an open work node with work children whose own parent is no open work
item. Its items are its direct children without Closed ones; an item's `keys` are
its own key and then its children's (`keys_total` when more than 20).

- **Counts**: Moving is a live session, In progress or QA, or an open pull request
  or review; Done is Done, Delivered or Accepted (or every leaf done); Paused is
  Backlog or work paused on purpose; everything else is Queued.
- **Progress** is computed, never set by hand: completed estimate hours over all
  estimate hours of the epic's leaves, each leaf once. A parent's own estimate and
  Closed leaves never count; without any estimate it is completed leaves over all
  leaves. `remaining_hours` is the open leaves' estimate hours.
- **LANDS** is the latest worker ETA over the open items, else the target release,
  else the suggested release. A suggestion is `~ Next` or `~ Next +N`: the stored
  suggested release, or, for a queued leaf the caller may read releases for, the
  visible queue wait plus the estimate and an hour of review placed on the release
  cadence. In the shared pool the wait counts the leaf's own project and divides
  by the parallel runs the account-use matrix allows there, like the Work list;
  without that capacity it stays blank. There is no separate ETA source; both
  use `eta.source: release`. A parent's own estimate is not a suggestion, and an
  item's suggestion does not roll up onto the epic. The epic uses its own target
  release, then its own stored suggestion, when no open item has a timed worker
  ETA.
- **Hide done** hides an epic only when the epic's own status is Done, Delivered
  or Accepted. An open epic whose children are all done stays listed and keeps
  its failing entry checks. `next_step` is the first open item by ETA, else the
  first of those checks.
- **Steps** are what is missing: the epic's failing entry checks for its next
  status (Ready from Open/Backlog/Blocked, QA from In progress, Done from QA) from
  the effective definition, in definition order and with their labels, then
  deliveries on hold and blockers. `no_active_build` is not evaluated in this read
  (it takes the builder fence of status and queue writes). No failing check and
  no hold or blocker means no steps.
- **Live line**: the working agent's activity note, else the item's latest
  unmerged pull request (`delivery`). Notes and session ids need `harness.read`.
  The author's name needs `harness.read` or `members.read`. The pull request
  state and a hold's key and reason need `delivery.read`. Counts stay either way.

Epics have no progress series or ETA history; their bar is a plain bar. A release
whose text starts with `~` is described as a suggested release; the rest are the
target release. The view re-reads the list on work events (node, status, merge,
`harness.heartbeat` and `delivery.state_changed`) as well as on goal events. A
Goals list does not re-read on those two. An epic with more than 200 items is
counts-only with `expansion: too_large`; discovery is bounded to 1000 epics.
