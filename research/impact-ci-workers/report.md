# AEON-1335 — S3 worker qualification: six-run analysis

**Recommendation: retain one Playwright worker; reject 6×2 and 4×3.** Keep the 12×1 control as the experiment reference, with all 31 groups effectively serial. Do not install 12 full-catalogue shards in production: that control also misses the required timeout margin. Preserve this checkout’s existing **17 CI shards and 24 nightly shards**, its impact-selected PR widths, and its production timeouts. No worker or shard-layout promotion is supported by these runs.

Analysis complete, 2026-10-17. This replaces the diagnostic round’s “qualification not run” status with the controller’s six measured restart runs. The analysis task is complete even though **S3 wider-worker qualification failed**. Release 137 build/review/release decisions remain with the coordinator. No product code, workflow, manifest, dependency, version, tracker status or remote state was changed. Machine-readable evidence, timestamps, case identities, per-spec checkpoints, artifact hashes and verdicts are in [measurements.json](measurements.json).

## Provenance and interpretation

All six runs used [`exp/aeon-1335-workers` at `3b298ea3976d05a56aac652c7a54141fdbbf129c`](https://github.com/inspr-at/paimos/tree/3b298ea3976d05a56aac652c7a54141fdbbf129c), attempt 1, on hosted Linux with the workflow’s four-vCPU assertion. They selected the same **5,005 unique registrations in 354 files**, with identical sorted case-key hashes across layouts. All 44 browser jobs started and uploaded tier checkpoints. That establishes setup and shard launch, **not successful completion of every test**. In fact, 18 browser steps timed out at the measurement-only 50-minute limit. No job reached the measurement-only 60-minute job limit.

The six 07:44–08:31 dispatches are excluded: web setup stopped on the experiment’s intentional departure from the production workflow shape. The valid runs start at 08:32 Vienna time (06:32 UTC), after the scratch-only `continue-on-error` change at `3b298ea39`. In the valid runs setup jobs conclude success, but their unit-step logs still contain the known `aeon-676-ci.test.mjs` shape failure and exit 1. The subsequent full `web-unit` tier command never produced its own measurement; uploaded unit artifacts contain test fixtures, not a completed full unit tier. These are browser experiments, not green release checks.

Run URLs below point to primary Actions evidence. Acquisition used GET-only `gh api` for run/job metadata, tier-measurement ZIPs, job logs and immutable source snapshots; local Git objects supplied the 354 policy records. Large trace/screenshot ZIPs were not downloaded. Tier artifacts expire after eight days; relevant measured values and hashes are retained in the JSON. The controller’s diagnostic report and `runs.txt` at `~/.aeon/lead/aeon/research/impact-ci-workers/` supplied the original stop/accounting rules and dispatch history.

## Accounting

- Queue-inclusive wall time is completed run `updated_at − created_at`; the separate last-job-completed timestamp is retained to make the completion proxy explicit. It includes setup and scheduling. “p95” means only the maximum of these two repeats, not a population percentile estimate.
- Job-minutes use each job’s `completed_at − started_at`. Browser totals include shard preparation, first pass, explicit diagnostics, artifact upload and post steps. All-job totals include both `experiment-plan` and `nightly-web-setup`. All started jobs/attempts are counted: **56 jobs, 1,834.22 all-job minutes**. There were no rerun attempts. These are elapsed minutes, not per-job rounded billing minutes.
- Pair candidate repeat 1 with control repeat 1, and repeat 2 with control repeat 2. Raw reductions are reported even where execution is incomplete; those reductions cannot qualify an unfinished layout.
- `run`, failures, skips and unresolved counts come from persisted first-pass checkpoints. A checkpoint is updated after a whole browser batch completes. **Unresolved includes any work executed but not yet reported inside the interrupted active batch**; it is not proof that every such registration never started. Candidate failure counts are therefore lower bounds, and rates describe only reported registrations.
- A flake is an exact first-pass failed identity that passes its explicit diagnostic retry. Persistent failures remain failures. Missing retries and missing/incomplete batches remain unknown. Never treat zero confirmed flakes as zero full-run flake rate.
- Summed native case durations are worker-seconds. Use the union of case execution intervals to calculate active wall time, then subtract that from the launch wall time. The experiment’s complete `experiment.json` files provide this correction. The tier report’s serial-only `nonCaseSeconds` is not valid for parallel launches.

## Run overview

| Layout / repeat | Actions run | Queue-inclusive wall min | Browser job-min | Plan + setup min | All job-min | Jobs | Complete shards | Reported run / unresolved | First-pass failures |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 12×1 repeat 1 | [39010289303](https://github.com/inspr-at/paimos/actions/runs/39010289303) | 44.53 | 382.95 | 8.72 | 391.67 | 14 | 12/12 | 4,870 / 0 | 6 |
| 12×1 repeat 2 | [39013064383](https://github.com/inspr-at/paimos/actions/runs/39013064383) | 46.78 | 391.00 | 8.93 | 399.93 | 14 | 12/12 | 4,870 / 0 | 6 |
| 6×2 repeat 1 | [39016091262](https://github.com/inspr-at/paimos/actions/runs/39016091262) | 60.08 | 304.13 | 8.57 | 312.70 | 8 | 1/6 | 2,904 / 2,018 | ≥6 |
| 6×2 repeat 2 | [39019890736](https://github.com/inspr-at/paimos/actions/runs/39019890736) | 59.75 | 294.60 | 8.20 | 302.80 | 8 | 1/6 | 3,025 / 1,880 | ≥12 |
| 4×3 repeat 1 | [39023717894](https://github.com/inspr-at/paimos/actions/runs/39023717894) | 60.08 | 204.82 | 8.47 | 213.28 | 6 | 0/4 | 2,073 / 2,851 | ≥10 |
| 4×3 repeat 2 | [39027569839](https://github.com/inspr-at/paimos/actions/runs/39027569839) | 60.25 | 205.12 | 8.72 | 213.83 | 6 | 0/4 | 2,096 / 2,845 | ≥3 |

Both controls report 4,870 executed registrations and 135 explicit skips; none is unresolved. Wider-layout wall times are the elapsed time to a failed, incomplete run and are already worse than control. Browser job-minutes are consumed cost, not an estimate of the cost to complete the missing work.

## Per-run job tables

All times below are minutes. `Run / skip / unresolved` refers to first-pass checkpoint registrations. Retry minutes are already inside the UI-step/job durations and are never added again. A dash means not applicable or unavailable. Job/API timestamps, source artifact IDs and diagnostic identities are retained per shard in the JSON.

### 12×1 repeat 1 — [39010289303](https://github.com/inspr-at/paimos/actions/runs/39010289303)

| Job | Complete job min | Browser step min | Run / skip / unresolved | First-pass failed | First-pass state / active batch |
|---|---:|---:|---:|---:|---|
| `experiment-plan` | 0.03 | — | — | — | complete |
| `nightly-web-setup` | 8.68 | — | — | — | API success; tolerated unit-step failure |
| Shard 1 | 31.55 | 30.78 | 283 / 0 / 0 | 0 | complete |
| Shard 2 | 34.53 | 33.77 | 450 / 55 / 0 | 0 | complete |
| Shard 3 | 35.67 | 35.03 | 422 / 17 / 0 | 0 | complete |
| Shard 4 | 24.60 | 24.02 | 376 / 0 / 0 | 0 | complete |
| Shard 5 | 35.17 | 34.45 | 525 / 4 / 0 | 2 | complete |
| Shard 6 | 28.05 | 27.35 | 447 / 1 / 0 | 0 | complete |
| Shard 7 | 34.25 | 33.57 | 452 / 1 / 0 | 0 | complete |
| Shard 8 | 35.30 | 34.52 | 532 / 9 / 0 | 0 | complete |
| Shard 9 | 34.57 | 33.90 | 348 / 18 / 0 | 0 | complete |
| Shard 10 | 23.88 | 23.15 | 396 / 26 / 0 | 0 | complete |
| Shard 11 | 31.02 | 30.25 | 342 / 4 / 0 | 4 | complete |
| Shard 12 | 34.37 | 33.70 | 297 / 0 / 0 | 0 | complete |

Longest / median shard job: **35.67 / 34.31 min = 1.040**. Worst production headroom: UI **-16.78%**, job **10.83%**. First-pass failures: `agents-dial-daily.spec.ts` ×4, `headings.spec.ts` ×2.

Diagnostics: the two `headings.spec.ts` identities both fail again; the four `agents-dial-daily.spec.ts` failures are not retried because this exceeds the three-case-per-shard retry cap. **0 confirmed fail→pass, four failures without diagnostics; flake evidence incomplete.**

Resources: final memory summaries 12/12; minimum reported free host memory 11.89 GiB; cgroup/OOM counters unavailable. 455 completed acquisition records, peak 12 browser processes; all recorded signal fields null and remaining-process counts zero. Terminal cleanup after timed-out steps is unknown. No explicit OOM/crash exit was found in these shard logs.

### 12×1 repeat 2 — [39013064383](https://github.com/inspr-at/paimos/actions/runs/39013064383)

| Job | Complete job min | Browser step min | Run / skip / unresolved | First-pass failed | First-pass state / active batch |
|---|---:|---:|---:|---:|---|
| `experiment-plan` | 0.08 | — | — | — | complete |
| `nightly-web-setup` | 8.85 | — | — | — | API success; tolerated unit-step failure |
| Shard 1 | 33.72 | 32.88 | 283 / 0 / 0 | 0 | complete |
| Shard 2 | 26.18 | 25.55 | 450 / 55 / 0 | 0 | complete |
| Shard 3 | 36.78 | 36.12 | 422 / 17 / 0 | 0 | complete |
| Shard 4 | 34.87 | 34.13 | 376 / 0 / 0 | 0 | complete |
| Shard 5 | 34.00 | 33.35 | 525 / 4 / 0 | 2 | complete |
| Shard 6 | 37.72 | 36.92 | 447 / 1 / 0 | 0 | complete |
| Shard 7 | 27.00 | 26.18 | 452 / 1 / 0 | 0 | complete |
| Shard 8 | 30.32 | 29.62 | 532 / 9 / 0 | 0 | complete |
| Shard 9 | 25.07 | 24.35 | 348 / 18 / 0 | 0 | complete |
| Shard 10 | 33.37 | 32.57 | 396 / 26 / 0 | 0 | complete |
| Shard 11 | 36.52 | 35.73 | 342 / 4 / 0 | 4 | complete |
| Shard 12 | 35.47 | 34.80 | 297 / 0 / 0 | 0 | complete |

Longest / median shard job: **37.72 / 33.86 min = 1.114**. Worst production headroom: UI **-23.06%**, job **5.71%**. First-pass failures: `agents-dial-daily.spec.ts` ×4, `headings.spec.ts` ×2.

Diagnostics: the two `headings.spec.ts` identities both fail again; the four `agents-dial-daily.spec.ts` failures are not retried because this exceeds the three-case-per-shard retry cap. **0 confirmed fail→pass, four failures without diagnostics; flake evidence incomplete.**

Resources: final memory summaries 12/12; minimum reported free host memory 11.46 GiB; cgroup/OOM counters unavailable. 455 completed acquisition records, peak 12 browser processes; all recorded signal fields null and remaining-process counts zero. Terminal cleanup after timed-out steps is unknown. No explicit OOM/crash exit was found in these shard logs.

### 6×2 repeat 1 — [39016091262](https://github.com/inspr-at/paimos/actions/runs/39016091262)

| Job | Complete job min | Browser step min | Run / skip / unresolved | First-pass failed | First-pass state / active batch |
|---|---:|---:|---:|---:|---|
| `experiment-plan` | 0.08 | — | — | — | complete |
| `nightly-web-setup` | 8.48 | — | — | — | API success; tolerated unit-step failure |
| Shard 1 | 51.30 | 50.20 | 120 / 0 / 509 | 0 | 50-min timeout; `attachment-security-combined` |
| Shard 2 | 51.02 | 50.20 | 660 / 59 / 171 | 2 | 50-min timeout; `decision-desk-combined` |
| Shard 3 | 51.40 | 50.20 | 742 / 24 / 172 | 2 | 50-min timeout; `decision-desk-combined` |
| Shard 4 | 48.10 | 47.30 | 864 / 0 / 0 | 2 | complete |
| Shard 5 | 51.18 | 50.22 | 216 / 0 / 681 | 0 | 50-min timeout; `planning-combined` |
| Shard 6 | 51.13 | 50.22 | 302 / 0 / 485 | 0 | 50-min timeout; `attachment-security-combined` |

Longest / median shard job: **51.40 / 51.16 min = 1.005**. Worst production headroom: UI **-67.39%**, job **-28.50%**. First-pass failures: `access.spec.ts` ×2, `header-glimpse.spec.ts` ×2, `hide-states.spec.ts` ×2.

Diagnostics: shard 4 retries the two `header-glimpse.spec.ts` failures. “hover shows Open graph and Pause; clicking the glimpse opens the graph” passes; “leaving the project releases the WebGL context” fails again. **One confirmed flake**, four other reported failures without diagnostics, five timed-out first passes. No full-run flake rate is available.

Resources: final memory summaries 1/6; minimum reported free host memory 11.46 GiB; cgroup/OOM counters unavailable. 363 completed acquisition records, peak 17 browser processes; all recorded signal fields null and remaining-process counts zero. Terminal cleanup after timed-out steps is unknown. No explicit OOM/crash exit was found in these shard logs.

### 6×2 repeat 2 — [39019890736](https://github.com/inspr-at/paimos/actions/runs/39019890736)

| Job | Complete job min | Browser step min | Run / skip / unresolved | First-pass failed | First-pass state / active batch |
|---|---:|---:|---:|---:|---|
| `experiment-plan` | 0.13 | — | — | — | complete |
| `nightly-web-setup` | 8.07 | — | — | — | API success; tolerated unit-step failure |
| Shard 1 | 51.20 | 50.22 | 120 / 0 / 509 | 0 | 50-min timeout; `attachment-security-combined` |
| Shard 2 | 51.20 | 50.20 | 660 / 59 / 171 | 6 | 50-min timeout; `decision-desk-combined` |
| Shard 3 | 51.03 | 50.22 | 742 / 24 / 172 | 2 | 50-min timeout; `decision-desk-combined` |
| Shard 4 | 51.42 | 50.20 | 321 / 0 / 543 | 2 | 50-min timeout; `attachment-security-combined` |
| Shard 5 | 38.58 | 37.82 | 880 / 17 / 0 | 2 | complete |
| Shard 6 | 51.17 | 50.20 | 302 / 0 / 485 | 0 | 50-min timeout; `attachment-security-combined` |

Longest / median shard job: **51.42 / 51.18 min = 1.005**. Worst production headroom: UI **-67.39%**, job **-28.54%**. First-pass failures: `access.spec.ts` ×1, `agent-pause.spec.ts` ×1, `graph-motion.spec.ts` ×1, `headings.spec.ts` ×2, `hide-states.spec.ts` ×6, `no-shift-agents.spec.ts` ×1.

Diagnostics: shard 5 retries both `headings.spec.ts` identities; both fail again. **0 confirmed fail→pass**, ten other reported failures without diagnostics, five timed-out first passes. No full-run flake rate is available.

Resources: final memory summaries 1/6; minimum reported free host memory 11.81 GiB; cgroup/OOM counters unavailable. 359 completed acquisition records, peak 14 browser processes; all recorded signal fields null and remaining-process counts zero. Terminal cleanup after timed-out steps is unknown. No explicit OOM/crash exit was found in these shard logs.

### 4×3 repeat 1 — [39023717894](https://github.com/inspr-at/paimos/actions/runs/39023717894)

| Job | Complete job min | Browser step min | Run / skip / unresolved | First-pass failed | First-pass state / active batch |
|---|---:|---:|---:|---:|---|
| `experiment-plan` | 0.03 | — | — | — | complete |
| `nightly-web-setup` | 8.43 | — | — | — | API success; tolerated unit-step failure |
| Shard 1 | 51.00 | 50.20 | 998 / 27 / 125 | 3 | 50-min timeout; `recurrences-combined` |
| Shard 2 | 51.03 | 50.20 | 398 / 0 / 890 | 1 | 50-min timeout; `planning-combined` |
| Shard 3 | 51.48 | 50.22 | 211 / 0 / 982 | 5 | 50-min timeout; `dispatch-combined` |
| Shard 4 | 51.30 | 50.22 | 466 / 54 / 854 | 1 | 50-min timeout; `attachment-security-combined` |

Longest / median shard job: **51.48 / 51.17 min = 1.006**. Worst production headroom: UI **-67.39%**, job **-28.71%**. First-pass failures: `access.spec.ts` ×3, `aeon-1071-chat-live.spec.ts` ×2, `aeon-1279-touch-scroll.spec.ts` ×1, `no-shift-agents.spec.ts` ×1, `relations.spec.ts` ×1, `views-filters.spec.ts` ×2.

Diagnostics: **none**. All four first passes time out, with no final experiment summaries; the reported failures and unresolved active batches have no diagnostic result.

Resources: final memory summaries 0/4; minimum reported free host memory unknown; cgroup/OOM counters unavailable. 306 completed acquisition records, peak 23 browser processes; all recorded signal fields null and remaining-process counts zero. Terminal cleanup after timed-out steps is unknown. No explicit OOM/crash exit was found in these shard logs.

Additional evidence loss: shard 1’s trace/screenshot upload fails with `ENOENT` while opening a disappearing trace `.network` file. Its tier checkpoint upload succeeds; the missing trace artifact is not an OOM diagnosis.

### 4×3 repeat 2 — [39027569839](https://github.com/inspr-at/paimos/actions/runs/39027569839)

| Job | Complete job min | Browser step min | Run / skip / unresolved | First-pass failed | First-pass state / active batch |
|---|---:|---:|---:|---:|---|
| `experiment-plan` | 0.07 | — | — | — | complete |
| `nightly-web-setup` | 8.65 | — | — | — | API success; tolerated unit-step failure |
| Shard 1 | 51.40 | 50.20 | 249 / 0 / 901 | 0 | 50-min timeout; `dispatch-combined` |
| Shard 2 | 51.37 | 50.22 | 398 / 0 / 890 | 1 | 50-min timeout; `planning-combined` |
| Shard 3 | 51.17 | 50.20 | 983 / 10 / 200 | 2 | 50-min timeout; `decision-desk-combined` |
| Shard 4 | 51.18 | 50.22 | 466 / 54 / 854 | 0 | 50-min timeout; `attachment-security-combined` |

Longest / median shard job: **51.40 / 51.27 min = 1.002**. Worst production headroom: UI **-67.39%**, job **-28.50%**. First-pass failures: `access.spec.ts` ×2, `no-shift-agents.spec.ts` ×1.

Diagnostics: **none**. All four first passes time out, with no final experiment summaries; the reported failures and unresolved active batches have no diagnostic result.

Resources: final memory summaries 0/4; minimum reported free host memory unknown; cgroup/OOM counters unavailable. 290 completed acquisition records, peak 23 browser processes; all recorded signal fields null and remaining-process counts zero. Terminal cleanup after timed-out steps is unknown. No explicit OOM/crash exit was found in these shard logs.

## Diagnostic cost and observed rates

The retry command wall times below exclude its containing job’s preparation and upload and are already included in job-minutes. Complete-run flake rates remain unknown.

| Layout / repeat | Reported executed | Recorded failures | Diagnostic cases | Retry command seconds | Confirmed fail→pass | Unretried recorded failures |
|---|---:|---:|---:|---:|---:|---:|
| 12×1 repeat 1 | 4870 | 6 | 2 | 13.896 | 0 | 4 |
| 12×1 repeat 2 | 4870 | 6 | 2 | 12.960 | 0 | 4 |
| 6×2 repeat 1 | 2904 | 6 | 2 | 19.091 | 1 | 4 |
| 6×2 repeat 2 | 3025 | 12 | 2 | 7.128 | 0 | 10 |
| 4×3 repeat 1 | 2073 | 10 | 0 | 0.000 | 0 | 10 |
| 4×3 repeat 2 | 2096 | 3 | 0 | 0.000 | 0 | 3 |

## Layout verdicts against the stop rules

| Criterion | 12×1 control | 6×2 | 4×3 |
|---|---|---|---|
| Browser runner-min ≤80% of paired control | Reference | Raw 79.42%, 75.35%; **not qualified**, incomplete work | Raw 53.48%, 52.46%; **not qualified**, incomplete work |
| All-job ratio, reported separately | Reference | 79.84%, 75.71% | 54.46%, 53.47% |
| Max-of-two queue-inclusive wall no worse | 46.78 min reference | **Fail:** ≥60.08 min (+28.43%) | **Fail:** ≥60.25 min (+28.79%) |
| First-pass fail→pass increase ≤1 pp | Unknown: four unretried failures per repeat | **Unproven:** partial first passes and diagnostics | **Unproven:** partial first passes, no diagnostics |
| Longest / median ≤1.25, each run | **Pass:** 1.040, 1.114 | Observed 1.005, 1.005; **not qualified**, cutoff-censored | Observed 1.006, 1.002; **not qualified**, cutoff-censored |
| ≥25% UI-step headroom vs 30 min | **Fail:** worst 36.92 min; −23.06% headroom | **Fail:** ≥50.22 min; ≤−67.39% | **Fail:** ≥50.22 min; ≤−67.39% |
| ≥25% job headroom vs 40 min | **Fail:** worst 37.72 min; 5.71% headroom | **Fail:** worst 51.42 min; −28.54% | **Fail:** worst 51.48 min; −28.71% |
| Complete first pass | 12/12 each repeat | **Fail:** 1/6 each repeat | **Fail:** 0/4 each repeat |
| Decision | Retain one-worker policy; not a qualified 12-shard full-suite rollout | **Reject** | **Reject** |

The 25% headroom rule means **UI ≤22.5 minutes and complete shard job ≤30 minutes**. Comparing against the experiment’s enlarged 50/60-minute limits would incorrectly pass the controls; those limits exist only to gather data. Even under a definition that requires budget ≥1.25×observed duration, all three layouts still fail. The destination regular-CI job limit is only 20 minutes, so the nightly full-catalogue results cannot certify a regular-CI layout.

Clipping five of six or four of four shards at the same timeout makes their longest/median ratios look excellent. This is censoring, not measured completion balance. Completing the missing work would add runner-minutes and might change both wall time and balance; no extrapolated savings or completion estimate is used.

Capacity: recorded run intervals do not overlap, consistent with the one-at-a-time dispatcher. Historical organization-wide queued-job snapshots were not included. The ticket records the ≤20 queued-job monitor, but this analysis cannot independently certify its historical observations. No new experimental load was added.

## Failure identity comparison and flake evidence

The following counts are failures in completed first-pass checkpoints, in repeat order. `0*` in a candidate layout means **no recorded failure**, not proof of a complete passing result for that spec. Per-spec selected/passed/failed/skipped/unresolved counts and every failed case key are retained in JSON.

| Spec | Group | 12×1 r1/r2 | 6×2 r1/r2 | 4×3 r1/r2 | Interpretation |
|---|---|---:|---:|---:|---|
| `access.spec.ts` | `access-dialogs` | 0/0 | 2/1 | 3/2 | Candidate-only observation; cause unproven |
| `aeon-1071-chat-live.spec.ts` | `remaining-ui` | 0/0 | 0*/0* | 2/0* | Candidate-only observation; cause unproven |
| `aeon-1279-touch-scroll.spec.ts` | `remaining-ui` | 0/0 | 0*/0* | 1/0* | Candidate-only observation; cause unproven |
| `agent-pause.spec.ts` | `agent-pause` | 0/0 | 0*/1 | 0*/0* | Candidate-only observation; cause unproven |
| `agents-dial-daily.spec.ts` | `remaining-ui` | 4/4 | 0*/0* | 0*/0* | Repeated in both controls |
| `graph-motion.spec.ts` | `remaining-ui` | 0/0 | 0*/1 | 0*/0* | Candidate-only observation; cause unproven |
| `header-glimpse.spec.ts` | `remaining-ui` | 0/0 | 2/0* | 0*/0* | Candidate-only observation; cause unproven |
| `headings.spec.ts` | `remaining-ui` | 2/2 | 0*/2 | 0*/0* | Repeated in both controls |
| `hide-states.spec.ts` | `remaining-ui` | 0/0 | 2/6 | 0*/0* | Candidate-only observation; cause unproven |
| `no-shift-agents.spec.ts` | `stable-controls` | 0/0 | 0*/1 | 1/1 | Candidate-only observation; cause unproven |
| `relations.spec.ts` | `stable-controls` | 0/0 | 0*/0* | 1/0* | Candidate-only observation; cause unproven |
| `views-filters.spec.ts` | `stable-controls` | 0/0 | 0*/0* | 2/0* | Candidate-only observation; cause unproven |

The baseline set is the exact same six identities in both controls: four phone control-position cases in `agents-dial-daily.spec.ts` (400 light/dark folding and 390 light/dark AEON-1166 detail) and two 390 light/dark session/account-heading cases in `headings.spec.ts`. The headings assert a panel-header height ≤93 and receive 117 in the controls and diagnostic retries. Those are repeatable baseline failures at the fixed SHA, not evidence of a worker-induced flake. The daily-dial failures lack diagnostic retries, so their flake classification remains open.

Candidate-only failed identities passed in both completed controls. The repeated `access.spec.ts` and `no-shift-agents.spec.ts` failures are native 30-second test timeouts; `hide-states.spec.ts` also times out, including reload/visibility waits. Other observed differences include chat write ordering, footer visibility, graph navigation and animation assertions. This is evidence correlated with layout/worker changes, **not proof of causation**: partitioning, batch order, runtime pressure and ordinary timing variation also changed. The two repeats and interrupted batches cannot separate these effects. `agent-pause` is serial even in the experiment, demonstrating that candidate-only failures need not imply a direct same-spec worker conflict.

Observed first-pass failure proportions on reported executed registrations are 0.1232%, 0.1232%, 0.2066%, 0.3967%, 0.4824%, 0.1431% in run order. The one confirmed fail→pass is 1/2,904 reported executions (0.0344%) in 6×2 repeat 1. These descriptive subset rates do **not** establish a full-run flake-rate increase ≤1 pp: both controls leave four failures without diagnostics, and every candidate run leaves execution unresolved. Neither unretried failures nor unresolved registrations are counted as passes or zero flakes.

## Why extra workers did not establish the expected speedup

The [supervisor at the experiment SHA](https://github.com/inspr-at/paimos/blob/3b298ea3976d05a56aac652c7a54141fdbbf129c/scripts/playwright-safe.mjs) freezes the native list, then awaits **one spec/repetition chunk at a time** before moving to the next. More workers can parallelize eligible tests within that chunk; they do not run different spec chunks concurrently. The experiment preserves supervision and uses this same behavior. The [tier batching policy](https://github.com/inspr-at/paimos/blob/3b298ea3976d05a56aac652c7a54141fdbbf129c/scripts/test-tiers/web-batches.mjs) also separates serial and candidate launch policies, including trace behavior. Cutting the shard count therefore increases serial work and per-chunk overhead per runner; proportional shard×worker arithmetic does not establish proportional throughput.

This is a source-supported explanation of a constraint, not a measured attribution of every lost minute. Native `parallelIndex` slots confirm widths 1/2/3 in reported eligible launches. Merged blob reports’ `config.workers` and console “using N workers” add workers across sequential chunk reports (for example, 18 in a serial launch); they must not be mistaken for simultaneous concurrency or used to diagnose an accidental 18-worker control. The final experiment records retain chosen effective worker counts separately.

Observed complete acquisition records show peak browser-process counts 12/12 for the controls, 17/14 for 6×2, and 23/23 for 4×3. These include browser subprocesses; they are not Playwright worker counts. No explicit OOM, exit 137/139, segmentation fault or non-null signal is recorded for completed shard acquisitions. All available final memory summaries lack cgroup counters; 18 timed-out shards also lack final memory/cleanup summaries. **No OOM observed** is the supported conclusion; **OOM ruled out** and **terminal cleanup verified** are not. Setup’s SIGTERM/SIGKILL lines belong to browser-free supervision test fixtures and are excluded from experiment-resource findings.

## Exact configuration handoff for the build round

**Layout patch: empty.** Adopt neither 6×2 nor 4×3, and do not copy the scratch nightly workflow into main. “Keep 12×1” means retain the experiment control’s one-worker policy, not replace current production shard counts with twelve.

| Surface | Exact resulting setting |
|---|---|
| `scripts/test-tiers/cli.mjs` | Keep forced `--workers=1`. Any separately implemented worker flag defaults to 1; production callers continue to select 1. No group is approved for a wider rollout. |
| `.github/workflows/ci.yml` | Preserve this checkout’s full fallback shards `[1, 2, …, 17]`, impact-provided PR widths, and 20-minute job budget. Experiment-source CI was 16 shards; do not revert the newer 17-shard layout. |
| `.github/workflows/nightly-full.yml` | Preserve `[1, 2, …, 24]` serial browser shards, 30-minute UI step and 40-minute job, existing triggers/aggregation and real unit failure handling. Never copy scratch 50/60-minute budgets or `continue-on-error`. |
| `web/ci-web-shards.json` and per-spec records | No shard/weight/launch-policy promotion from censored measurements. Preserve current performance/isolation policies. |

All 31 groups remain effectively one-worker in production. The experiment already held these 23 groups serial: `release`, `service-tier`, `key-layout`, `work-queue`, `recurrences`, `decision-desk`, `status-help`, `model-settings`, `status-autopilot`, `performance`, `project-header`, `needs-attention`, `aeon-624-dialogs`, `theme-agents`, `phone-approvals`, `agent-pause`, `decision-knowledge`, `webcore-recovery`, `work-node-integration`, `settings-shared`, `coarse-hit-area`, `lead-flow`, `usage-dashboard`.

The eight experiment candidates also remain one-worker pending successful requalification: `access-dialogs`, `planning`, `dispatch`, `stable-controls`, `attachment-security`, `footer`, `clipped-names-and-preferences`, `remaining-ui`. Performance and other unsafe groups must remain serial in any future experiment.

A later build may independently correct interval-union accounting and persist chunk-level observations, without changing layout or success semantics. Fresh worker qualification must use the current destination catalogue and serial shard baseline, complete every first pass and bounded diagnostic, preserve ownership/supervision, and pass every stop rule. Neither a new worker flag nor a different shard count is validated by this report. No such implementation is part of this analysis change.

## Validation and ownership

PPM account `workstation-agents`, INSPR tenant, verified before material work. Coordinator attribution for session `aeon-1335-s3-analysis` (`aee471df-7881-49d7-80bf-9ace7e824ddd`) was already recorded on AEON-1335; no new tracker write was needed. Only this worktree’s research outputs and ignored `.agent-status.json` were written.

Validation recomputes job durations from all 56 API records; requires six fixed-SHA, one-attempt runs and every expected shard; checks identical 5,005-key catalogues, per-tier accounting, native failure identities, diagnostic fail→pass identities, raw paired ratios, timeout logs and headroom; parses the final JSON and checks the report values. `git diff --cached --check` passed before commit. No browser or product tests are needed for the research-only file change.

`node scripts/ci-static.mjs --merge-main` was **not run and is not green**: its full path creates another worktree and generates source/build output beyond this read-only analysis brief’s write set. This is a disclosed handoff limitation, not a claim of static CI success. No push, dispatch, deployment, model CLI, review gate, PR, merge, experiment-branch deletion or work in another worktree occurred. The coordinator owns cross-family review after handoff.
