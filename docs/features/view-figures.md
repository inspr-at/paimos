<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# View figures API

`GET /api/projects/{projectId}/figures` supplies the header work bar and column
figures for AEON-1358. Its source is the approved AEON-1356 r4b specification
(SHA-256 `51f7cec9a60db81c384d23fc7eee0f2a3eb598126807b210a2263c81be7e363e`).

Schedule this request after the list's first paint, using the **same resolved
node-list query** (`ticketList.apiParams(projectId, filters)`). Resolve saved
views through the existing saved-view loader before building that query. Search,
status selections, exclusions, dates, tags, assignees and other list filters use
the existing node-list SQL. The path confines the request to a visible project;
`within` can narrow it to a subtree of that project. Without an explicit kind
filter, it selects work and the legacy ticket/task/epic aliases.

The endpoint deliberately ignores `hide_closed`, `hide_states`, sorting, facets,
page size and cursor. It aggregates **every matching row**, including rows on
later pages and rows hidden by Hide closed. Its `scope` contains the matching
row/parent/leaf counts, matching Closed leaves, and all project leaves including
Closed for “N of M tickets”. Render footer wording such as “incl. hidden rows”
from the current list presentation, without refetching figures for Hide toggles.

The header groups contain raw hours, ticket counts and canonical status
breakdowns. Cancelled and Archived contribute no header/Estimate/Progress hours;
parents never contribute their own planned hours or rolled-up hours. Missing or
invalid leaf estimates contribute zero and have a separate count. `approximate`
marks totals made from estimates; `open_estimated_leaves` identifies the estimates
that still belong to Open/In progress work. Format and round the raw figures in
the UI, putting `~` on totals only.

`marks_releases` tests visible project status-change events across the previous
90 days, independently of view filters. When false, Done joins `completed`,
`done_not_live` stays empty, and the client labels the first group “Done”.

`columns` provides Key, Status, Priority, Assignee, Epic, Release, Suggested
release, Tags, Cost unit, Model, Estimate, Tokens, Cost, Created, Updated, ETA and
Progress. Key and date figures use all rows; other columns use leaves. Tokens
and Cost retain Closed work and provide separate measured and estimated cohorts
(a measured value wins over that row's estimate). `partial` exposes unreported
usage or incomplete pricing/planning evidence. Cost and per-unit cost are
projected only with `harness.read`; amounts are API list-price USD. Top-N
categories retain their tail as `others` and missing values as `without_value`.
Tokens/Cost also provide the median and average of displayed values as `typical`,
marked approximate when estimates occur in that sample, without a mixed total.
Model counts follow the list's primary used model, then its frozen planned
route or current planned route when there is no used model.
ETA's next/last finish are the earliest/latest reported ready estimates, with
their ticket keys. Progress includes only leaves with a progress reading and
weights readings with valid hour estimates; no weighted percentage is invented
when those estimates are absent. Date ages are seconds as of `computed_at`.

The response is one immutable object from a repeatable-read tenant/RLS snapshot.
Keep the previous complete object while a changed scope loads, then replace it
in one render. Hovers read that object and do no computation or network work.
There are no UI changes in this server package.

The module keeps at most 256 cached scopes for 30 seconds. Keys partition tenant,
principal, key metadata, current visible projects, projection permissions and
effective filters. Every read reauthorizes before looking in the cache. The
tenant event watermark invalidates cached responses after node, estimate, status
or other tenant events; the age limit also refreshes live telemetry and ages.
No aggregate work is added to `GET /api/nodes`. Computation has a 20-second
deadline, a 50,000-row limit and a 32 MiB metadata budget (64 KiB per row).
Existing ETA/planning root limits are respected by processing leaves in batches
of 200. Budget failures return an error, never silently truncated totals.
