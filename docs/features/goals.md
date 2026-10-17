<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Goals API

AEON-1456 adds project and workspace goals, with outcomes stored as ordinary
child nodes. Existing release nodes can also be read and updated as goals;
release membership and release publication are separate concerns. No sample
data is created. The tenant goal/outcome kinds are restored lazily on an
authorized create or fresh update, including a release's first outcome.

The contract is authored in `api/areas/goals.yaml` and served with
`Aeon-Contract: goals/1.4`. It provides:

- `POST /api/goals` for a goal title and optional project UUID.
- `PATCH /api/goals/{key-or-id}` for ETA, steps, a flagged progress override,
  and a bounded batch of outcome upserts, removals, moves and handover changes.
- `GET /api/goals` and `GET /api/goals/{key-or-id}` for filtered state, progress,
  estimate coverage, effective ETA, passed, project badges, questions and stall.
- `GET /api/goals/{key-or-id}/history` for descending event history. An
  `outcome` UUID keeps the same timeline after a move.
- `GET /api/goals/{key-or-id}/series?range=24h|7d|30d` for recorded progress.

Writes require `nodes.write` in each affected goal's current scope. A required
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
