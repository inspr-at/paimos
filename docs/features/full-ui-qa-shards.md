<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Full UI QA file assignment (AEON-1726)

Full UI QA uses `scripts/ci/full-ui-qa-shards.json` for its five UI partitions.
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
| 1 | 78 | 3 | 83.873 |
| 2 | 79 | 3 | 83.872 |
| 3 | 79 | 1 | 83.871 |
| 4 | 82 | 1 | 83.861 |
| 5 | 79 | 2 | 83.863 |

All ten audit files are spread across the five partitions (`ui-audit.spec.ts` is
the light-390 audit). Of 349 files observed in the latest run, 295 change their
recorded shard ownership; the other 48 have no completed latest placement.

**The requested 60-minute target cannot be met by balancing five single workers.**
The full timing inventory totals 409.340 test minutes: even perfect packing needs
83.868 minutes per shard including the reserve. Even discarding all 48 historical
rows leaves 360.711 minutes of completed current work, already over the aggregate
300-minute target before setup. `--check` reports `withinTarget: false` explicitly;
its zero exit status proves inventory coverage, not the runtime target. These
projections leave about six minutes under the unchanged 90-minute job timeout,
with uncertainty from the older estimates. The requested 60-minute acceptance
remains unmet; more concurrent workers or a separate runtime reduction would be
needed. OPS owns the actual workflow-dispatch proof after the PR exists.
