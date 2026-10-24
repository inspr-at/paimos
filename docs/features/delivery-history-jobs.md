<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Delivery history and project lifecycle

## Least-privilege history research (AEON-1253)

The [Checks API](https://docs.github.com/en/rest/checks/runs) exposes check names,
conclusions and started/completed timestamps with checks read. The
[commit listing](https://docs.github.com/en/rest/commits/commits) needs contents
read. These can reconstruct observations on default-branch commits, recent PR
heads and currently reachable merge-group heads. The pinned default-branch head
is always queried, even when its commit predates the history window; suite
timestamps decide which observations belong in the window. Known CI aggregates
(`go`, `web`, `release-check`, `e2e`, `migration-compat`) and their `nightly-`
counterparts identify candidate workflows in the repository's YAML files at
each suite's commit. A shared fact uses the unique matching repository path,
independently of any project's workflow filters. Multiple matching files,
unsupported definitions and oversized snapshots withhold the mapping. This
identification is an inference; arbitrary workflow names cannot be resolved
from checks alone. Workflow reads cover at most 20 directory entries and
128 KiB per file. Unchanged workflow blob identities reuse the mapping across
commits and persisted continuation steps.

PR open/merge timestamps and reported release/review facts stay exact. Individual
check timestamps and conclusions are exact observations. Workflow wall time is
an approximate envelope from the earliest observed check start to the latest
completion; nightly outcomes are observed suite verdicts, without an exact event
or scheduled-run preference. Historical PR revisions, deleted queue heads,
workflow attempts and the queue time before a job starts are not recoverable.
First-attempt success, required checks on attempt one, time to first green,
flakes, queue-run counts/ejections, preflight and runner wait are withheld for
checks-derived records. Workflow coverage remains partial, including on days
without observations. Exact Actions facts supersede only observations with the
same verified check-suite ID, workflow and head. Separate nights on one commit
remain observations when only one corresponding Actions run is available.
Older Actions rows without a verified suite ID cannot remove observations.
The nullable `delivery_metric_runs.check_suite_id` column (migration 1345) keeps
that identity through webhook/backfill upserts and reads; it is metadata in the
DSAR inventory.

Projects apply their workflow filters when metrics are read. Changing a filter
changes that project's visible samples while existing repository facts keep
their original workflow identity.

[Installation metadata](https://docs.github.com/en/rest/apps/apps#get-an-installation-for-the-authenticated-app)
reports the granted permissions. A repository-scoped token requests only
checks/contents/pull_requests/metadata read, adding Actions read only when already
granted. No new credential or write grant is needed; tokens are revoked after
the bounded read. `history_source` records the source of the last successful step.
The Repository card shows it below its controls; checks explicitly names partial
history and unavailable attempt/runner-wait metrics. Longer source text grows
downward without moving Change, More or the history links.
Grant changes restart the selected history window with the appropriate reader.

The 90-day rate estimate is
`ceil(C/100) + C + S + ceil(P/100) + H + Q + D + F + R + 3`
repository reads when suites fit on one page: C default-branch commits including
the pinned head, S their and the other heads' matching suites, P recent PRs,
H their heads, Q live queue heads, D workflow-directory snapshots, F workflow
files read when their blob identities change, and R directory retries when a
changed snapshot must resume in the next step. Extra check pages add calls.
Identical commit trees reuse the whole snapshot. At 50 commits/day, 50 recent
PR heads/day, one suite/head, no live queue heads, and 19 unchanged workflow
files, this is about 27,112 reads (D = 9,000, F = 19, R = 0). Each step is limited
to 40 repository reads, with one minute between unfinished steps, plus
installation lookup, token creation and revocation. Thus automatic continuation
for one continuously active project uses at most about 2,580 calls/hour (at
least about eleven hours for that example; reserved read capacity and network
time can make it longer),
below the [minimum installation budget of 5,000/hour](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
Other projects, live reads and explicit Check again retries share that budget;
Check again bypasses ordinary pacing, while a GitHub rate-limit deadline
remains binding. GitHub refusals retain a classified reason and persisted backoff. Lists have explicit bounds;
a bound never turns missing history into success or zero.

Linking a delivery metric source schedules history reading on the server. Changing
its repository or workflows starts a new history window (1–90 days, default 30).
The existing backfill endpoint still returns one bounded step and also schedules
continuation. `restart` replaces the history window. Progress survives leaving
the page and restarting the server; each step stores its cursor and facts together.
Continuation with omitted `days` and Check again retain the selected window,
including after a first automatic read fails before committing a cursor. Once
a cursor exists, selecting another window requires `restart`.

AEON-1710 stores a read watermark and a fixed upper time bound in the existing
cursor JSON. Finished sources refresh every fifteen minutes; Check again can
start that incremental refresh immediately. Ordinary continuation of a finished
job remains a no-op. Actions listings read only the created-time interval after
the previous watermark. Unfinished runs retain bounded IDs and are followed on
the next refresh, so a late completion is not lost behind the time filter.
Checks history uses the new interval for commit listing, suite selection and
updated pulls; its existing coverage limitations remain explicit. Repeated
Actions pages reuse contiguous stored attempts. Job reads remain restricted to
missing first-attempt job maps for the selected CI workflow's PR/queue runs.
Validated pages and their retry position commit together even if a later read
fails. These fields survive a cold start without introducing a new table or
changing the user-visible delivery-settings revision.

The request-count regression covers 186 stored CI runs, matching the incident-day
count, and a 900-run stress fixture. Both add three new runs and one late
completion, with two attempts per run, and reload the cursor between every
bounded step. The test prints repository requests before and after the
incremental refresh; installation lookup, token creation and revocation add
three requests per bounded step.

## Job and attempt ingestion (AEON-1658)

Completed `workflow_run` deliveries now write exact run attempts into
`delivery_metric_runs`, including earlier attempts returned by Actions. Completed
GitHub Actions `check_run` deliveries resolve the same run identity only when
the payload's suite status is completed; earlier checks wait for `check_suite`
completion. A completed job never stands in for an unfinished workflow. Job
maps are read only after the workflow completes, and retain every conclusion
within the existing 500-job bound. Redelivery and repeated backfill pages converge
on `(tenant, repository, run, attempt)`.

The installation-bound `workflow_job` queued, waiting, in-progress and completed
events store only run/attempt/job IDs and queue/start timestamps in
`delivery_metric_jobs` (expansion 1422). No logs, steps, runner names or runner
labels are projected. The complete REST job list supplies job identities, so
early or late job events can fill missing queue timestamps. Completed job events
open a GitHub read only to repair an existing run attempt without a complete job
map; events before workflow completion and events with a complete map store
timestamps locally. Runner wait is withheld while any non-skipped job's wait is
unknown. Historical jobs without queue timestamps stay partial; backfill does not
invent a queue time from the workflow's start. Confirmed flaky executions remain
withheld under the existing measurement contract: a successful rerun is a workflow
rescue, not proof that the same case, workflow revision and runner class failed
and passed.

A failed best-effort live job read wakes the durable history worker, including
after an earlier history window finished. Each jobs step makes at most 40 reads,
including pagination; an attempt that crosses the budget is retried whole.
Full-budget unfinished Actions steps pause for one minute. GitHub rate-limit
responses retain their bounded Retry-After/reset hint in the existing persisted
retry deadline. The jobs pass reads only the selected history window. Authority
and source revision are checked again under the tenant fence before committing
backfilled facts.

### Coordinator backfill on pm.barta.cm

After deployment, use an existing PPM identity with `delivery.manage` on AEON
and the App's already-granted Actions read capability. This worker does not
trigger production repairs. No new key is needed. The existing endpoint starts
one bounded step and the server continues the durable job.

Live attempt ingestion needs the App's `workflow_run` subscription; live runner
queue times also need `workflow_job` delivery. The coordinator checks those
subscriptions through the existing App administration path when verifying
coverage. This repository change does not alter host-owned App settings.

Start and inspect the seven-day window:

```sh
/Users/markus/.aeon/lead/aeon/pm --instance ppm me
/Users/markus/.aeon/lead/aeon/pm --instance ppm curl /api/projects/23e3f45b-27e4-43c7-985b-bb0c66179fb3/delivery/metrics/backfill -X POST --data '{"days":7,"restart":true}'
/Users/markus/.aeon/lead/aeon/pm --instance ppm curl /api/projects/23e3f45b-27e4-43c7-985b-bb0c66179fb3/delivery/metrics
```

Read `source.backfill`, `source.history_source`, `source.last_read_error` and
`source.retry_at` to follow progress. An explicit continuation POST uses `{}`;
honour `retry_at` and stop only at `state: done`. Repeating the same completed
window is idempotent. Checks-only history cannot recover attempts or runner
waits, and remains explicitly partial. During implementation the assigned
worker's live metrics GET returned 403 (`agent key scope required`), so its
production cursor, App grants and job population could not be inspected; no
claim is made that the production backfill had never run. The code-level gaps
were workflow-run metrics being skipped, workflow-job ingress being refused,
and failed live job reads not rescheduling a completed backfill.

The source exposes `last_read_error` (reason, missing permission when known,
check time, optional `status_code` and `reset_at`), `retry_at`, and `last_event_at`.
An Actions 403 that is not rate limiting becomes `actions_read_missing`; a missing
installation/repository becomes `not_installed`. Rate limits use `rate_limited`,
request timeouts `timed_out`, other HTTP errors `github_error` with the status,
and transport failures `unreachable`. Legacy or unclassified failures retain
`github_unavailable`; the UI explains that no specific cause was recorded.
English and German copy names each known cause and keeps the stored history.

Permission and installation failures start at 30 minutes; other non-transient
failures start at one minute. Repeated failures double the persisted delay,
capped at one hour. Transient failures retry with jitter after 30–59 seconds.
Rate-limit Retry-After/reset hints instead set the exact persisted retry time,
including a deadline beyond an hour. Successful responses with at most 100
requests remaining stop further history reads and resume at reset. Missing
hints retain a bounded fallback. Check again and restart cannot bypass a stored
rate-limit deadline. Partial job reads use the same policy, and failed runs
become eligible at the source's stored deadline. A successful step clears its
read error. Signed, installation-bound repository webhooks update the last event
time; clients consider seven days without an event stale. A linked source
without any webhook has a null event time.

Each classified history failure logs the endpoint family, status, parsed
remaining/reset hints, timeout flag and duration. Error strings, URLs, queries,
response text and credentials never enter that diagnostic.

The job stores only the initiating principal and authenticating key metadata IDs.
It reloads current authority and checks `delivery.manage` again under the tenant
fence after each GitHub read. Revocation prevents continuation. Source revisions
prevent an old request from overwriting a changed source. All progress and
error/event fields retain tenant/project row-level isolation.

Frozen and archived projects admit no new lead starts/dispatch, routine work or
queue claims/starts. The admission reason is `project_frozen` or
`project_archived`. Existing processes retain ownership and can report completion;
people can continue editing. Project deletion additionally requires a live
project Admin or workspace Owner/Admin role. A requested project key is reserved
within its tenant, including deleted projects. Creation returns 409 with the
visible holder's project name; hidden holders produce a generic reservation
conflict. Historical imported alias collisions remain readable.
