# Account quota history (AEON-1499)

The data API retains measured Claude/Codex weekly and five-hour quota for the
current account binding. It does not enable unqualified idle vendor launches.
Claude 2.1.296 and codex-cli 0.162.1 remain unqualified: live quota-neutral
startup, account identity, configuration/tool isolation and cleanup evidence
was unavailable under the worker's prohibition on calling model CLIs.

## Idle executable qualification and pins (AEON-1514)

The reviewed table is `productionCapacityPins()` in
`internal/agentd/capacity_pins.go`. It is empty in this release. Agentd loads
Codex capabilities from it at adapter construction and verifies the physical
path and SHA-256 again at use time, using the file's device, inode, size,
modification time and change time to cache only unchanged bytes. Unknown
paths, versions, digests and changed files retain `unsupported` and the
existing fallback reason, without launching a process. Version is the OPS
qualification label bound to the pinned bytes; no `--version` process is run
to qualify an unknown executable.

The capacity executable is independent of the enrolled run/probe wrapper.
For Codex it must be the physical native binary: agentd executes that path as
`app-server --listen stdio://`, with no wrapper, `codex.js`, Node or
`NODE_OPTIONS`. The child receives only its selected private `HOME` and
`CODEX_HOME`, a bounded `PATH`, `LANG=C` and `LC_ALL=C`. Existing account
identity checks, deadlines, owned process-group termination and cleanup fences
still apply. Helpers are pinned only if the qualified exchange spawns them;
native Codex has no Node helper. Claude/Grok capability checks also bind to
their own executable pins. Claude still has no production idle
exchange/decoder; adding a pin cannot supply that missing implementation.
Pi's existing OpenRouter key-cap API is independent of this executable table.

OPS performs live qualification only after AEON-1516's preconditions are met.
The evidence must cover the exact version/path/bytes, the quota-neutral
exchange and decoder, account identity, startup hooks, inherited
configuration/tools, every spawned helper, and confirmed owned-group cleanup.
Synthetic fixtures are protocol evidence only. No production digest was
collected by this implementation.

OPS hands over one entry per qualified executable in this exact Go format
(placeholders below are not production values), together with the evidence
reference:

```go
{Harness: Codex, Version: "<exact qualified version>",
 Path: "<absolute physical native executable path>",
 SHA256: "<64 lowercase hex characters>",
 Helpers: []capacityHelperPin{}},
```

When an exchange needs helpers, each helper has
`{Name: "<role>", Path: "<absolute physical executable path>", SHA256: "<64 lowercase hex characters>"}`;
`Name: "node"` denotes a qualified interpreter for an exchange that needs one.
The reviewed follow-up adds the evidence-backed entry to the checked-in
table; there is no enrollment, environment or local-config enable switch.
An executable update, path change, helper change or new version requires
fresh qualification and a reviewed pin entry. The coordinator owns review,
merge and release; OPS evidence alone does not alter a running daemon.

`GET /api/agent-accounts/{accountId}/quota-series` requires person
`account.read`, or the registering agent's `account.probe`. Paired runtime keys
remain limited to their own connected enrollment. Owner usage sharing applies
to both the latest sample and history, including for administrators. Private
or missing quota returns empty arrays. A changed ownership binding hides the
previous binding's quota; quota never grants execution consent.

The required `from` (inclusive) and `to` (exclusive) are RFC3339 UTC hour
boundaries, at most 31 days apart. `window_kind=weekly` or `window_kind=5h`
filters readings; omit it for both. Pages contain at most `limit` hours
(default and maximum 168). `next_cursor` is the exclusive hour boundary for
the next page; retain the account, range and filter. It is empty when complete.
Hours are ascending; an hour may have no readings for the selected window.
`latest_readings` is independent of the requested history range.

```http
GET /api/agent-accounts/{accountId}/quota-series?from=2026-10-18T00:00:00Z&to=2026-10-19T00:00:00Z&window_kind=weekly&limit=24
```

The response contains `account_id`, `latest_readings`, `hourly_readings` and
`next_cursor`. Each hourly entry has `hour_at` and `readings`. Each reading
contains `window_kind`, `bucket`, `used_percent`, `remaining_percent`, `read_at`,
`resets_at`, `source`, `reading_age_seconds` and `freshness`. Used/remaining are
the last measured values, including when stale or expired. Consumers must show
the reading's age and reset rather than interpreting stale values as present
headroom. Missing measurements stay unknown. AEON-1497 owns their presentation.

The existing probe transport keeps the latest normalized snapshot on the
account and upserts one `account_quota_hourly` row per account and UTC hour,
with up to 32 window/bucket samples. A sample is assigned to its original
reading hour, including delayed delivery after restart. The last reading per
window wins; equal timestamps prefer harness over agentd. Replays do not create
additional rows or overwrite newer samples. Sparse updates preserve other
windows. The response calculates remaining percentage and age from stored
facts; it never extrapolates consumption or replenishment.

Only current local subscription-resource facts contribute idle samples, so a
shared resource does not attribute another login's probe to this account.
Existing normalized run readings also populate the series; estimates do not.
Idle reports stay out of append-only reading history and do not add per-probe
`account.probed` events. Health probes also retain their latest facts on the account row; account.probed is appended only for a visible availability, readiness, warning-level or error-class change (AEON-1354).
No vendor responses, account identities, paths, plans or credential material
are stored in the new quota snapshots. Migration 1363 is expand-only, adds a
nullable account snapshot and a FORCE RLS hourly table, and classifies both in
the DSAR inventory. The adapter follows the account, keeps both JSON snapshots
in review, exports only the hour boundary, and treats the binding revision as
metadata. Existing history is neither backfilled nor deleted.

Verification on 2026-10-18 used the approved remote test runner. Full account,
daemon, pairing and privacy suites passed on `8efda1236`; auth, reporter
contract and DSAR suites passed on `b95bcc261` with their implementation
unchanged afterward. The strict authorization policy fixture was updated for
the one added route and `TestDomainPolicyComposition` passed on `88c67ec4c`.
The negative control on a separate scratch branch disabled quota storage and
failed `TestIdleQuotaProbeHourlyRoundTrip` at the expected zero-hourly-rows
assertion. This is data-path and synthetic capability evidence; it does not
qualify either live vendor binary.
The final guarded `ci-static --merge-main` run on `88c67ec4c` exited 0:
44 checks passed with no optional skips, including migration, ownership,
manifest and generated-contract checks. No origin push, deployment or model
review gate was performed. Production idle qualification remains open.

## Event telemetry retention (AEON-1354)

Account probes and harness heartbeats retain current facts on their existing rows. Routine timestamps, sequence increments and changing quota percentages do not create before/after event rows. Visible readiness, availability, quota warning or error changes and real harness phase, activity, estimate or metadata changes still create event rows. Retention keeps only part of them beyond 14 days: heartbeat rows that change a session's phase, role, bound ticket, stop or archive stay indefinitely, because goal history replays them. Every `account.probed` row and every other heartbeat row, activity, estimate and metadata changes included, is deleted once it is older than 14 days. Harness activity and metadata histories keep their existing storage. Stall detection reads session heartbeat timestamps directly.

Live-mode event streams receive a `harness.heartbeat` SSE wake hint after a committed quiet heartbeat, when the viewer may read that session. A hint has data `{}` and no event ID; it carries no session facts and advances no resume cursor. The live node store gathers hints for 10 seconds after the first one, then hints every work row a view holds, as a heartbeat event with an ID does for its ticket: ticket lists and the outline reread the ETA and lead worker of their rows in one batched list read, and an open parent ticket rereads its aggregate projection. `/agents` subscribes with `telemetry=false`, so there the hints only feed the stream-health watchdog; its freshness comes from the 20-second poll. Durable state-change events retain their existing shape.

A server job runs on startup and daily. It deletes only `account.probed` events and quiet `harness.heartbeat` events, those whose before and after snapshots agree on session, phase, role, bound ticket, stop and archive, older than the fixed 14-day server retention. It works in batches of at most 500, with a 100 ms wait between batches and per-type counts in server logs, and removes each batch's goal history references before the events. One SQL rule decides eligibility for the batch selection, the delete policy, the append-only trigger and the partial index. The job checks tenant boundaries and skips a tenant if eligible telemetry participates in undo. Other event types remain indefinitely. The migration creates the narrow retention authority and a concurrent partial index; it performs no cleanup. The release flow takes a fresh backup before starting the new binary. No `VACUUM FULL` or table rewrite is used; ordinary autovacuum and subsequent writes can reuse space. OPS measures the post-release table size.

Quiet probe/heartbeat streams now produce zero event rows per week unless their visible state changes. Applying the measured 88% telemetry share to the observed 411 thousand events/week suggests roughly 49 thousand other events/week, plus genuine state transitions, if that traffic mix persists. The historical quiet baseline was 1–4 thousand rows/week. These are estimates; production size and growth verification belongs to the release/OPS follow-up.

Validation for AEON-1354: six named regressions are registered as ESSENTIAL, covering ordinary and restart probes, changed/quiet session heartbeats, real SSE delivery, commit/rollback notifications, bounded idempotent retention, undo protection and injected startup/daily scheduling. The database-free scheduling test passed and compiled `internal/events`; all 43 migration policy tests, 357 tier-tool tests and 33 ownership audit tests passed. Remote database suites and full static validation were refused for host capacity (first YELLOW/not GREEN, then 2/2 occupied remote slots); they remain required in PR CI. No production cleanup or size measurement has run in this worker.
