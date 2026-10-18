# Account quota history (AEON-1499)

The data API retains measured Claude/Codex weekly and five-hour quota for the
current account binding. It does not enable unqualified idle vendor launches.
Claude 2.1.296 and codex-cli 0.162.1 remain unqualified: live quota-neutral
startup, account identity, configuration/tool isolation and cleanup evidence
was unavailable under the worker's prohibition on calling model CLIs.

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
`account.probed` events. Health probe telemetry is outside this ticket's scope.
No vendor responses, account identities, paths, plans or credential material
are stored in the new quota snapshots. Migration 1363 is expand-only, adds a
nullable account snapshot and a FORCE RLS hourly table, and classifies both in
the DSAR inventory. Existing history is neither backfilled nor deleted.

Verification on 2026-10-18 used the approved mbp2606 runner. Full account,
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
