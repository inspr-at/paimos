# Time in status: data and API (AEON-1369)

## Real-data prerequisite

Read-only audit of `ppm`, tenant INSPR, acting principal `workstation-agents`,
2026-10-14 22:22:52–22:26 UTC. The assigned worker marker was verified on
AEON-1369. No production writes, source-system requests or telemetry were used.
Design truth is AEON-1357's r4b HTML (SHA-256
`aa73166662dc7e0897d80c1f6e8228e8ecb8f83d16cb56c9f80d1b762341d0cd`)
and r4b specification (SHA-256
`51f7cec9a60db81c384d23fc7eee0f2a3eb598126807b210a2263c81be7e363e`).
Its illustrative histories, actors and releases are synthesized, as explicitly
stated in the attached decisions; they are not audit evidence.

| Measure (live work leaves; parents excluded) | AIT | AEON |
| --- | ---: | ---: |
| Leaves | 174 | 1,286 |
| Native creation baseline | 151 (86.78%) | 1,124 (87.40%) |
| Migrated leaves | 23 | 162 |
| Migrated leaves with retained source history | 23/23 (100%) | 162/162 (100%) |
| Any native or imported baseline | 174/174 (100%) | 1,286/1,286 (100%) |
| Continuous observed chain through current status | 174/174 (100%) | 1,286/1,286 (100%) |
| Leaves with an actual status transition | 173/174 (99.43%) | 1,142/1,286 (88.80%) |
| Native transitions (canonical spelling) | 269 | 3,057 |
| Status changes between retained source snapshots | 30 | 143 |
| Total status transitions | 299 | 3,200 |
| Missing source actor IDs **and** names | 0 | 0 |

AIT's oldest leaf was created at **2026-09-07 10:59:26 UTC**; its oldest leaf
status transition is **2026-09-07 14:16:21 UTC**. AEON's corresponding dates are
**2026-09-23 11:39:43 UTC** and **2026-09-23 12:54:46 UTC**. The oldest AIT
`import.history` event on any project node is 2026-09-07 08:16:19 UTC; that is
not the leaf coverage start. Import ingestion starts on 23 September, but
retained `import.history.record.snapshot.status` and `changed_at` preserve
earlier source history. Ignoring those records would falsely reject coverage.

There are **no observed interior state discontinuities** after canonicalizing
`in-progress`/`in_progress` and other supported aliases. The apparent gap on
AEON-1256 is event **864575**, `queue.added`, at
2026-10-12 18:24:43.661551 UTC: In progress → Open. It is a genuine node-state
mutation, not queue telemetry. Native transition types found were
`node.updated` (AIT 266; AEON 2,947), `status_autopilot.changed` (3; 109),
and `queue.added` (0; 1).

Imported initial snapshots have small timestamp offsets: AIT 9 at creation,
11 at +1 second, 3 at +2 seconds; AEON 16 at creation, 26 at +1 second,
118 at +2 seconds, 2 at +3 seconds. The first source snapshot establishes an
initial state, not an invented transition. Aligning this initial state to the
retained creation timestamp is a disclosed assumption; the offset is returned
by the lifeline. Longer initial gaps must be treated as inferred. A matching
observed chain cannot prove that no unlogged change ever happened.

**Prerequisite result:** sufficient retained evidence to implement the API,
including source-history replay. Missing creation/source history is not present
in this audited corpus. Existing skips are retained as skips; no QA, pickup,
release or acceptance transition may be invented. At audit time AIT has no
Delivered/Accepted leaves, so its final metric is Created → Done.

### Reproduction

Use `/Users/markus/.local/share/aeon-cli/aeon --instance ppm me` to verify the
account, then `aeon curl` with that same absolute executable and instance:

1. Resolve each project with `GET /api/projects/lookup?ref=AIT` (then AEON).
2. Read all `/api/nodes?within=<project UUID>&limit=200&sort=key&direction=asc`
   pages through `next_cursor`, without Hide closed; keep live work leaves.
3. Read `/api/events?project_id=<UUID>&limit=200&after=<ID>&type=<types>` to
   exhaustion through `next_after`. Eight names fit each type-filter batch.
   Include node creation/update, import node snapshots/parent changes,
   `import.history`, status-autopilot change/derived/undo/attention events,
   `delivery.merge_done`, `queue.added` and `queue.add_undone`.
4. Compare imported snapshots in `(at,id)` order and native before/after states
   in the same order. Ignore unchanged canonical statuses. Join the last source
   snapshot to the first native before-state and reconcile the final state
   against the node. Count tickets separately from transition events.

The audit read 8 AIT and 51 AEON bounded event pages. All continuations were
exhausted. Only node IDs/keys, statuses, timestamps and actor-presence information
were retained in local ignored scratch files; bodies and credential material
were not retained. Reads used the existing RLS-visible API. The capture is not
a database-wide atomic snapshot; repeat it if live edits change the corpus.

## API behavior

The authored contract is `api/areas/projects-status-timing.yaml`. GETs never
write. Event and node visibility plus current project permissions apply inside
one tenant read snapshot. Durations use seconds; p50/p90 use nearest rank.
Imported snapshots are replayed, not replaced with ingestion timestamps.
Telemetry is not a source. Reopened and backward visits remain separate.

Explicit baseline backfill records only a labelled assumption for leaves with
no usable status history. It does not alter ticket state or claim that the
recording principal was a historical mover. Replays append no duplicate event.
Inferred or discontinuous histories are excluded from percentile samples.
Parents never contribute to project metrics.

- `GET /api/projects/{projectId}/delivery/tickets?window=90&type=all&priority=all&worked_by=all`
  returns current/previous per-step samples, nearest-rank p50/p90, rework count,
  coverage and daily/weekly/monthly buckets. The window is 7, 30, 90, 180 or
  365 days; a step belongs to `[now-window, now)` by its ending timestamp.
- `GET /api/nodes/{nodeId}/lifeline` returns separate status visits with entry
  and exit actors, optional release, repeated/ongoing flags and source labels.
  A current terminal status has zero duration; its preceding visits retain
  their time. The UI obtains usual/slow values from the unfiltered 90-day API.
- `POST /api/projects/{projectId}/delivery/tickets/backfill` accepts
  `{"limit":100}` and then the returned UUID `cursor` until `next_cursor` is
  null. It appends `node.status_history_backfilled` only for history-free
  leaves. The node namespace preserves existing project event visibility for
  subsequent editors. Permission changes and concurrent backfills serialize
  through the tenant/tree fence, followed by sorted row locks and the event
  counter last. No new tables, permissions, migrations or production backfill
  are needed.

GETs reject over-budget work rather than returning truncated metrics: 5,000
leaves / 100,000 timing events per project, 2,000 events per lifeline, and a
five-second deadline. Coverage is explicitly partial for absent/inferred
histories, visible chain discontinuities or a window before retained history.
Existing event RLS can hide an event (for example another actor's private queue
event); the API preserves that boundary and excludes a resulting discontinuous
history. Source actor names without a tenant-local identity mapping are kept
with kind `unknown`, so worker filters never guess person/agent provenance.
