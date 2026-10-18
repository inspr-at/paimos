# Impact CI: planning and capacity

AEON-1337 runs classification, tier scheduling, PR checkout identity and shadow
publication in one `ci-plan` job. It shares one checkout and Node setup, removing
one hosted job start from the planning dependency chain. The job publishes the
effective lane from the tiers step; its trusted base classifier supplies only
the raw lane, spec list and Nix selection. The runner router and trusted reuse
verifier continue to start independently. Their authority and checkout trust
boundaries stay separate.

The measured tuning decision is to retain `max_entries_to_build=6`. The live
organization is on Enterprise with 500 standard hosted slots, and OPS-324
already applied the queue's 6/6 settings on 2026-10-17. Admission and runner
queueing are short in the measured window. The observation and local proposal
are in [impact-planning-baseline.json](../../scripts/ci/impact-planning-baseline.json);
no live settings or pool pins were changed.

The required checks remain `go`, `web`, `release-check`, `e2e` and
`migration-compat`. Explicitly planned PR skips remain valid; a failed,
cancelled, absent or unexpectedly skipped prerequisite fails its aggregate.
`CI_IMPACT=off` bypasses base classification and publishes full mode, layout,
lane, browser scope and all 17 browser shards. Merge-group, main and manual
events retain the full gate. No S4 browser area is activated.

Reuse recognizes the consolidated suite and requires both `Classify local PR
diff` and `Plan test tiers` to have succeeded, as well as every existing full
execution marker. A prior split-planner suite is outside this inventory and
fails back to fresh validation. During adoption an older trusted base verifier
can likewise reject the new inventory and run fresh tests. No affected or
partial execution becomes full proof.

`test-tier-run-measurement` records each job's `runner_queue_seconds`
(`created_at` to `started_at`) and `execution_seconds` (`started_at` to
`completed_at`) alongside runner minutes. Planned skips and explicitly
runnerless cancellations have no execution sample. Current and historical
planning job names contribute to planning runner cost.

Organization capacity and queue admission are collected with the existing
read-only tool:

```sh
node scripts/ci-queue-measure.mjs --slots \
  --from 2026-10-17T00:00:00Z --to 2026-10-17T23:40:00Z \
  --capacity 500 --safety-slots 2 --save tmp/slot-snapshot.json > tmp/slot-report.json
node scripts/ci-queue-measure.mjs --slots --input tmp/slot-snapshot.json \
  --capacity 500 --safety-slots 2 > tmp/slot-replay.json
```

Collection includes every visible organization repository, every workflow,
all attempts, PRs, nightly and release work. A 24-hour lookback and separate
active-run queries cover jobs already running when the window opens. Response,
pagination, run, job, timeline and total collection bounds reject truncated
evidence. Visibility and lookback limits are explicit in the report. Unknown
runner classes prevent a recommendation; self-hosted jobs are counted
separately from the supplied hosted-slot budget.

The report separates three phases:

- Admission: queue enqueue event to creation of the first observed CI run in
  that queue session. Rebuilds, absent sessions and sessions predating the
  lookback have no admission sample.
- Runner queueing: each job's creation to runner start, including downstream
  fan-out. This is separate from PR residence in the merge queue.
- Execution: runner start to completion. Unfinished execution has no completed
  duration sample.

Queueing and execution distributions in the capacity report sample hosted jobs
starting within the window. The existing per-run CI artifact includes all
runner classes.

Partial reruns can expose new API job IDs and creation times with copied old
execution timestamps. The tool excludes a retained row only when its name,
runner, start/end times, conclusion and full-tier marker match an earlier
attempt exactly; the report identifies both source and retained rows. An
unproven reversed timestamp fails analysis. Explicit runnerless cancellations
have no execution interval, including synthetic rows created after cancellation.

The capacity calculation uses simultaneous intervals, including runnable
waiting jobs. It reserves the time-weighted p95 demand from all work except
this repository's merge-group CI, plus the supplied safety reserve. It divides
the remaining slots by the p95 peak demand of observed full queue builds.
At least five builds with the current 7 hosted Go shards (or 4 self-hosted Go
shards), 4 unit and 17 browser execution markers are required. A reused or
partial run cannot make a full build appear
cheap. An undersized sample or a budget that cannot fit one build yields no
concurrency ceiling. The supplied capacity is mandatory and is checked before
collection; an obsolete default cannot choose a tuning value. A historical estimate is a budget for
the observed mix; it does not establish a throughput improvement.

The ruleset tooling prepares local candidate and rollback payloads only. An
operator refreshes the live snapshot and checks the exact diff before applying
it. `max_entries_to_build` controls concurrent builds; merge limits are a
separate setting ([GitHub queue semantics](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)).
Required checks, ALLGREEN grouping, merge method, merge limits, timeout,
conditions and bypass actors must survive the tuning change. Revert the
planning workflow change to restore the split jobs, or use `CI_IMPACT=off` to
restore full execution without changing required checks. The saved ruleset
snapshot supplies the concurrency rollback.

Capacity and admission regressions run through the existing `ci-plan-tests`
static entry, alongside planner and reuse checks. Selector, kill-switch and
measurement integration remain in the existing fixed tier regression entry.
No native Go/web cases or browser launch records are added, so no tier rows are
needed. The existing `scripts/**` ownership slice covers the tooling and its
measurement baseline. The static registry removes the retired `tier-plan` job
and pins the consolidated planner's command inventory.

The 2026-10-17 UTC window runs from 00:00 to 23:40, including the scheduled
nightly. Collection covered 22 visible repositories, 1,297 workflow runs
created in that window and 2,680 histories including the preceding day.
Twenty-two queue builds proved the current full matrices. Their p95 peak
demand was 33 hosted slots; competing runnable demand had a time-weighted p95
of 88 slots. Hosted execution peaked at 153 simultaneous jobs. No self-hosted
jobs occupied runners in this sample.

| Phase | Samples | p50 seconds | p95 seconds |
|---|---:|---:|---:|
| Admission to first CI run | 34 sessions | 18 | 19 |
| Hosted runner queueing | 11,377 jobs | 2 | 3 |
| Hosted execution | 11,376 completed jobs | 239 | 944 |

The live org API reports `plan.name=enterprise`.
[GitHub's current standard-runner limit is 500 for Enterprise](https://docs.github.com/en/actions/reference/limits#job-concurrency-limits-for-github-hosted-runners).
The older 60-slot concept baseline no longer describes this organization.
The capacity ceiling is `floor((500 - 88 - 2) / 33) = 12` builds. Configured
concurrency six uses a modeled `6 × 33 + 88 + 2 = 288` slots. These observations
support retaining six: the low admission and runner waits provide no measured
throughput benefit for raising it to the ceiling. The proposal corrects the
old two-build file to the already-applied six-build setting, and its live
dry-run has no differences. Any future ruleset change also needs OPS's
lockstep pool-pin and generation update; that ownership remains outside this
worktree.

The baseline records all three latency populations, exact full-build run/SHAs,
the raw snapshot hash and replay command. Retained API copies (310 rows) and
runnerless cancellations (253 rows) are accounted for without fabricated
execution. These counts cover the collected histories, including lookback.
The full snapshot remains under `tmp/` for the coordinator and is excluded
from Git. The next observation should compare a similar mix after the planner
merge; no post-change speedup is claimed by this baseline.
