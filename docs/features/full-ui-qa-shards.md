<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Full UI QA file assignment (AEON-1726, AEON-1729)

Full UI QA uses `scripts/ci/full-ui-qa-shards.json` for its eight UI partitions.
The performance partition retains its existing config and Playwright partition.
`ci.yml`, its tier policy, browser specs and assertions are unchanged.

The guard discovers every `**/*.spec.ts` under `web/tests`, except
`**/performance.spec.ts`, exactly as the unchanged UI config specifies. Full UI QA
assigns all 378 non-visual files exactly once. Its explicit `excluded` list names
all 19 `tests/visual/**` files, including `reference-guard.spec.ts`; the guard and
hosted runner report each as `EXCLUDED`. Excluding any non-visual file fails.
Missing, duplicate, stale, invalid or both assigned-and-excluded entries fail
before Playwright launches. A new visual spec needs an explicit exclusion entry.
Changes to the config's file selectors also require an explicit guard update.

Each launch uses only the assigned files as anchored, escaped absolute file
patterns, so no `tests/visual/**` file reaches the Full UI QA invocation. This
exclusion applies only to Full UI QA; both shared Playwright configs are unchanged.
The existing `ci.yml` job `visual-comparison` owns visual checkpoints in the
digest-pinned renderer, and `visual-capture.yml` job `visual-reference-capture`
owns reference capture. Neither workflow changes here. This separation follows
the OPS proof run 40085074161: all 87 failures were renderer identity refusals in
19 visual files; no non-visual tests failed and no shard was cancelled.

One worker, zero retries, forbid-only, the existing reporters and trace policy
remain in force. Playwright's `--shard` is omitted for UI files because the
assignment has already partitioned them. A failing child remains a failing shard.

Run these browser-free checks from the repository root:

```sh
node --test scripts/full-ui-qa.test.mjs
node scripts/full-ui-qa-shard.mjs --check
```

The guard and its missing/duplicate/wrongly-excluded negative controls run in the existing
`full-ui-qa-tests` static check, already invoked by `ci.yml` and ci-static. These
script tests are outside the Go/web tier catalogue; no tier entries change.
The new files are covered by audit slice S7's existing `scripts/**` and `docs/**`
ownership. To update a timing or add a spec, update its source evidence and row,
then run `node scripts/full-ui-qa-shard.mjs --rebalance`. This uses the canonical
whole-owner writer in `scripts/test-tiers/core.mjs`, independently of PR shards.
If that duration balance leaves a partition without an audit, a deterministic
swap exchanges an audit from a partition with several for the closest-duration
one- or two-file bundle. This preserves every timing and gives each partition
an audit without concentrating the slow audit files.

## Timing evidence and limits

Source: [Full UI QA run 40074634146](https://github.com/inspr-at/paimos/actions/runs/40074634146),
commit `1f9813f37f0edade4851f97d8c067abe0c55b58f`.
The checked-in assignment records artifact IDs, durations and prior shard ownership
per file. Durations aggregate all nested specs and results by reporter file path;
files split across the old partitions are combined. The current UI config receives
`fullyParallel` through `browserPolicy`, and three files occur in multiple green
artifacts; the new assignment does not depend on Playwright's partitioning behavior.

| Old shard | Artifact | Test-case minutes | Playwright wall minutes |
| --- | --- | ---: | ---: |
| 1 | 12554796148 | 68.043 | 68.508 |
| 2 | 12555035765 | 75.926 | 76.560 |
| 3 | 12554317115 | 60.332 | 60.910 |
| 4 | 12554637037 | 70.542 | 71.627 |
| 5 | 12554783748 | No final JSON: cancelled | 89.650 before cancellation |

Shard 5's archive was inspected: it has no `full-ui-results.json`. Its
[job log](https://github.com/inspr-at/paimos/actions/runs/40074634146/job/120815298595)
retains 47 complete file windows before the interrupted light-1920 audit. The
interval from each line-reporter test start to the next start is charged to the
preceding file; the final interrupted file is excluded. These are labeled
`latest-complete-log-file` (46 files), or `latest-json-and-log` (one boundary file),
rather than presented as complete JSON reports. Another 302 files use latest JSON.

For files without latest-run measurements, the assignment snapshots the existing per-file
`nightlyTiming` records under `web/scripts/ci-web-shard-specs`. Their earlier
source is [nightly run 38732586462](https://github.com/inspr-at/paimos/actions/runs/38732586462),
whose retained measurement artifacts 11974037356 and 11975905656 were inspected.
They retain the completed-batch evidence used by that timing refresh. These rows
are labeled `prior-nightly`: they include historical measurements and estimates,
rather than new successful Full QA measurements. After excluding the 19 visual
files, 29 retained rows use prior nightly timings, contributing 34.796 minutes;
they need refreshing from the next complete run. Every retained timing and source
record is unchanged from AEON-1726. The excluded files contributed 13.833 minutes.

Setup in the latest five jobs took 34–35 seconds before Playwright. The projection
reserves 120 seconds per shard for setup, launch/non-case work and artifact upload;
the largest successful Playwright non-case residual was 65.108 seconds. No tests,
retry settings or timeouts were reduced to improve the projection.

| New shard | Files | Audit files | Projected minutes including reserve |
| --- | ---: | ---: | ---: |
| 1 | 45 | 1 | 51.446 |
| 2 | 47 | 1 | 51.439 |
| 3 | 47 | 1 | 51.441 |
| 4 | 47 | 1 | 51.436 |
| 5 | 47 | 1 | 51.424 |
| 6 | 47 | 3 | 51.444 |
| 7 | 48 | 1 | 51.453 |
| 8 | 50 | 1 | 51.423 |

All ten audit files are spread across the eight partitions (`ui-audit.spec.ts` is
the light-390 audit). The retained inventory totals 395.507 test minutes;
29 files still use prior nightly timings rather than completed latest placement.

**OPS retains the existing eight shards for the 60-minute target.** The eight-shard
assignment projects at most 51.453 minutes including setup, with at least one
audit on every shard. The workflow/guard tests require every projection to meet
the target; `--check` reports `withinTarget: true` for all eight. One worker,
zero retries, forbid-only and the 90-minute timeout remain unchanged. These are
projections with uncertainty from the older timings. OPS owns workflow-dispatch
acceptance: a green Full UI QA run with no cancelled shard on this branch.
