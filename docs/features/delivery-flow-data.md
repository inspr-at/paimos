# Delivery Flow data

Delivery › Flow (AEON-994) shows what is moving right now, what it waits for
and when it will be done. AEON-1004 records the data behind it; the lanes,
Replay and Compare views follow in later packages.

A **run** is a release (`ref` 126) or a change (`ref` AEON-991). A run has
**steps** and **incidents**. Each step names its `step_key` (release steps `a`
to `l`, `copy_gate`, `pin_gate`, `build`, `review`, `ci`, `queue`,
`merge_round`, `hold`, `mitigation`, `switch`, `live_check`), its round, its
kind (`work`, `wait`, `rework`, `recovery`), and its actor (person, agent, CI
or merge queue, with a role label). A wait names its reason and what it waits
for. Steps record timing facts only. They never store titles of findings,
logs or credentials.

## Sources

- **OPS rollout record.** `POST /api/projects/{projectId}/delivery/flow/rollout`
  takes the coordinator's `aeon.rollout.v1` record with `delivery.manage`. The
  record adds `steps`, `incidents`, `next_human_gate` and an optional OPS
  `eta`. It is authoritative: reporting the same record again changes nothing,
  and a corrected record updates the same rows. Other rollout fields, such as
  digests, pins and qualification, are accepted and not stored. Labels and
  summaries that look like a credential are refused. The run ends at
  `healthy_at`, or at `live_at` if no healthy time is reported.
- **PAIMOS work.** A projector follows the event log after a stored cursor per
  workspace, about every 10 seconds. It turns work-queue rounds into steps:
  first build to `build`, fix rounds to `build` repeats, merge rounds to
  `merge_round` and land rounds to `queue`. Review gates become a reviewer
  wait and a review step with the verdict. Delivery holds become waits, and a
  merge ends the change. Replaying the log converges on the same rows.
- **GitHub App.** Completed CI runs of a pull request that PAIMOS links to a
  ticket become `ci` steps. Merge-group runs become `queue` steps, with the
  wait in the queue before the run. A failed attempt that a later attempt
  passed on the same commit is `flaky`; the re-run is a repeat. Only the
  project's CI workflow counts.

## Reading

`GET /api/projects/{projectId}/delivery/flow?at=&from=&to=` returns the runs
active in the window (default: the last 45 minutes). It includes the steps
and incidents that touch the window. For each run it adds `pct_done`, the
current step, the next human gate and the ETA at the moment `at`.
`GET /api/projects/{projectId}/delivery/flow/runs/{itemId}` returns one
whole run for Replay and Compare. Both need `delivery.read` on the project.
Other projects' members get 404, and row security keeps the rows inside the
project. Results are bounded to 50 runs and 4000 steps, and `truncated`
reports a cut.

`pct_done` is the share of the critical path that is done: `a` to `l` for a
release, and `build`, `review`, `ci`, `queue` for a change. The ETA adds the
p50 and p90 of the remaining steps, from finished steps with the same
`step_key` in the last 30 days, minus the time the open step has already run.
If a remaining step has fewer than 3 finished runs, the basis is `none` and the
reason names the step. An OPS estimate in the rollout record has basis `ops`.
Each step's `norm` carries the usual p50/p90 and Arion's target where one
exists (checks 8, merge queue 7, review 8 minutes). A release's target is 24
minutes from step `a`.

## Live

`GET /api/projects/{projectId}/delivery/flow/stream` sends server-sent events
`delivery.step`, `delivery.item` and `delivery.incident` after each commit.
Each frame carries only ids (`item_id`, `step_id` or `incident_id`), so the
page refetches the flow or the run. `delivery.read` is re-checked on every
15-second heartbeat. A connection lasts at most 30 minutes and resumes with
`Last-Event-ID`. Agents can also read the same hints through
`/api/events/subscribe?topics=delivery`.
