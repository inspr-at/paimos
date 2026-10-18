# Merge-queue correlation

AEON-1020 / Project Arion WP1.6 produces daily, replayable queue evidence for
Delivery. It observes GitHub; it does not change checks, admission, queue
configuration, ALLGREEN, runners, or release gates.

`queue-correlation.yml` runs at 04:43 UTC and on manual dispatch. It publishes
the previous seven completed UTC days as a job summary and a private
`queue-correlation` artifact, retained for 30 days. The artifact contains
`snapshot.json` (source evidence), `report.json` (the machine-readable Delivery
feed), and `report.md` (the daily table). No PAIMOS credential is needed. The
Delivery metrics API does not yet ingest this artifact; its consumer belongs
to the Delivery page work package. The report schema is
`aeon.queue-correlation.v1`; `daily` carries counts and runner-wait samples,
`additional_runs` carries classification evidence, and `coverage` names the
number of joined group identities and PR timelines.

The collector reads workflow runs and attempt-1 jobs through REST GETs. Fixed
GraphQL queries read PR queue additions/removals and, where exposed, removal
reason and `beforeCommit`. GraphQL transports read queries using POST; there
is no mutation path. Only queue events are requested, without PR titles,
bodies, review text or actor identity. The snapshot retains PR numbers,
SHAs, event IDs, times, conclusions and required-check names.

A queue branch provides the PR number and immediate base SHA. The collector
checks those against the synthetic merge commit and follows first parents to
find entries ahead, ordered nearest main first. An ancestor remains an entry
ahead only while its PR queue session was open when the run was created. An
already-merged ancestor requires an exact commit-bound merge event; missing
or inconsistent evidence makes the group incomplete. The report keeps an
incomplete run with warnings and never invents entries ahead.

Classification applies to the earlier run in a rebuild pair for a PR merged
inside the window. Required-red non-cancelled runs remain a separate baseline.
Predecessor failure requires a `failed_checks` removal for the exact queued
ancestor and an unchanged PR head. Manual removal requires a `manual` event
bound to the old group or a queued ancestor. Reordering requires an explicit
commit-bound reorder reason, or the same queued PRs in a changed ancestor
order with an unchanged PR head and queue session. Cancellation, a moving
base, changed membership, unrelated events and conflicting causes stay
unknown. Counts of observed removals by the API's reason are separate from
inferred required-red ejections.

Runner wait is the maximum attempt-1 job `created_at` → `started_at`, excluding
skipped jobs. It is not time the PR spent in the merge queue. Each daily point
includes sample count, p50, p90 and maximum in seconds. Percentiles use linear
interpolation, matching Arion's measurement. Empty samples have null values.
Live completion uses job completion timestamps; the W2 fixture preserves the
original measurement's completion fields for exact population replay.

Run an explicit window locally with existing `gh` authentication:

```sh
node scripts/ci-queue-correlate.mjs \
  --from 2026-10-05T00:00:00Z --to 2026-10-09T04:00:00Z \
  --out tmp/queue-correlation
```

Offline replay, without GitHub access:

```sh
node scripts/ci-queue-correlate.mjs \
  --input scripts/testdata/ci-queue-correlation-w2.json \
  --from 2026-10-05T00:00:00Z --to 2026-10-09T04:00:00Z
node --test scripts/ci-queue-correlate.test.mjs
```

W2 reproduces **90 inferred ejections**, **24 additional runs**, **44 extra
runs after required red**, and **151 merged PRs with queue runs** before
classification. All 263 runs created in W2 have joined group identities.
The 24 additional runs become **12 predecessor failures, 6 manual removals,
0 proven reorderings and 6 unknowns**. The reordering test permutes recorded
API shapes deterministically; it does not claim a historical W2 reordering.

The collector bounds windows (30 days), lookback (one day), runs (5,000),
pages (20 per resource), queue ancestry (100), timeline PRs (2,000), commits
(10,000), input/output files (64 MiB), each API response (8 MiB / 60 seconds)
and total collection (20 minutes). Pagination exhaustion, GraphQL partial
errors, inconsistent identities and the Actions search cap fail publication.
A requested offline window must lie within the snapshot's coverage.
`--required-checks` overrides the recorded five-check baseline deliberately;
it does not modify the repository's required checks.

The seven-day post-deployment outcome owner is AEON LEAD, with OPS responsible
for daily workflow availability and the Delivery page owner for consumption.
Post the sample count, group/timeline coverage, unclassified share and rebuilds
by cause with worst-job runner-wait p90 on AEON-1020. Compare against W2 on the
same definitions; do not claim zero unknowns or tune the queue from missing
evidence.
