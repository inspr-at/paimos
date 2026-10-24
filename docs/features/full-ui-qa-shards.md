<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Full UI QA file assignment (AEON-1726)

Full UI QA uses `scripts/ci/full-ui-qa-shards.json` for its eight UI partitions.
The performance partition retains its existing config and Playwright partition.
`ci.yml`, its tier policy, browser specs and assertions are unchanged.

The assignment includes every `**/*.spec.ts` under `web/tests`, including nested
visual specs, except `**/performance.spec.ts`, exactly as the UI config specifies.
The runner checks that inventory before launching Playwright and rejects missing,
duplicate, stale or invalid assignments. Changes to the config's file selectors
also require an explicit guard update. Each launch uses anchored, escaped absolute
file patterns, one worker, zero retries, forbid-only, the existing reporters and
trace policy. Playwright's `--shard` is omitted for UI files because the assignment
has already partitioned them. A failing child remains a failing shard.

Run these browser-free checks from the repository root:

```sh
node --test scripts/full-ui-qa.test.mjs
node scripts/full-ui-qa-shard.mjs --check
```

The guard and its missing/duplicate negative controls run in the existing
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

For the remaining 48 files, the assignment snapshots the existing per-file
`nightlyTiming` records under `web/scripts/ci-web-shard-specs`. Their earlier
source is [nightly run 38732586462](https://github.com/inspr-at/paimos/actions/runs/38732586462),
whose retained measurement artifacts 11974037356 and 11975905656 were inspected.
They retain the completed-batch evidence used by that timing refresh. These rows
are labeled `prior-nightly`: they include historical measurements and estimates,
especially visual files, rather than new successful Full QA measurements. They
contribute 48.629 minutes and need refreshing from the next complete run.

Setup in the latest five jobs took 34–35 seconds before Playwright. The projection
reserves 120 seconds per shard for setup, launch/non-case work and artifact upload;
the largest successful Playwright non-case residual was 65.108 seconds. No tests,
retry settings or timeouts were reduced to improve the projection.

| New shard | Files | Audit files | Projected minutes including reserve |
| --- | ---: | ---: | ---: |
| 1 | 47 | 1 | 53.169 |
| 2 | 49 | 1 | 53.173 |
| 3 | 50 | 1 | 53.177 |
| 4 | 49 | 1 | 53.164 |
| 5 | 50 | 1 | 53.164 |
| 6 | 49 | 3 | 53.170 |
| 7 | 51 | 1 | 53.164 |
| 8 | 52 | 1 | 53.159 |

All ten audit files are spread across the eight partitions (`ui-audit.spec.ts` is
the light-390 audit). The inventory and its 409.340 test minutes are unchanged;
48 files still use prior nightly timings rather than completed latest placement.

**Eight shards are required for the 60-minute target with the setup reserve.**
OPS authorized seven shards with an eight-shard fallback in fix1. Even perfect
seven-way packing needs 409.340 / 7 + 2 = 60.477 minutes per shard. The eight-shard
assignment projects at most 53.177 minutes including setup, with at least one
audit on every shard. The workflow/guard tests require every projection to meet
the target; `--check` reports `withinTarget: true` for all eight. One worker,
zero retries, forbid-only and the 90-minute timeout remain unchanged. These are
projections with uncertainty from the older timings, not a measured successful
eight-shard run. OPS owns workflow-dispatch proof after the PR exists.
