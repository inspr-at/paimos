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
Check again deliberately bypasses the delay. GitHub refusals retain the
existing error classification and persisted backoff. Lists have explicit bounds;
a bound never turns missing history into success or zero.

Linking a delivery metric source schedules history reading on the server. Changing
its repository or workflows starts a new history window (1–90 days, default 30).
The existing backfill endpoint still returns one bounded step and also schedules
continuation. `restart` replaces the history window. Progress survives leaving
the page and restarting the server; each step stores its cursor and facts together.
Continuation with omitted `days` and Check again retain the selected window,
including after a first automatic read fails before committing a cursor. Once
a cursor exists, selecting another window requires `restart`.

The source exposes `last_read_error` (reason, missing permission when known, and
check time), `retry_at`, and `last_event_at`. Any GitHub 403 from an Actions endpoint
becomes `actions_read_missing`; a missing installation/repository becomes
`not_installed`; other read failures become `github_unavailable`. Permission and
installation failures start at 30 minutes; other non-transient failures start at
one minute. Repeated failures double the persisted delay, capped at one hour.
Transient failures retry with jitter after 30–59 seconds. Partial job reads use
the same policy, and failed runs become eligible at the source's stored deadline.
The server honours these times. `POST /projects/{projectId}/delivery/metrics/check-again` bypasses the
retry time immediately without discarding progress; it requires `delivery.manage`.
A successful step clears its read error. Signed, installation-bound repository
webhooks update the last event time; clients consider seven days without an event
stale. A linked source without any webhook has a null event time.

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
