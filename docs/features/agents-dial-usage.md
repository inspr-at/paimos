# Usage in the Agents dial (AEON-1497)

The open Agents dial reads quota first, per harness: how long the pooled
quota lasts, quota left over time, and each subscription as a battery. The
folded dial carries a small quota-left line for Claude and Codex. Settings ›
Accounts is reached by one link ("Accounts and limits → Settings"); there is
no separate usage page. Design: Tick's approved fuel gauge attached to the
ticket (`aeon-1497-dial-usage-approved.html`, `build-notes.md`).

## Sources

`GET /api/agent-accounts/quota-outlook` (person `account.read`; agent keys
are refused; no parameters) reads the caller's own accounts, the same family
as the dial's daily limits, grouped by harness. Owner usage sharing applies:
a private account keeps null numbers. At most 64 accounts, 16 per harness,
are read; `truncated` says when more exist.

Readings come from the AEON-1499 hourly rollup (`account_quota_hourly`) and
the latest snapshot (`agent_accounts.quota_latest`) of the current binding.
Per account the binding weekly bucket is the most used one that has not
expired. History covers the last 7 days in UTC hour slots; the projection
covers the next 7 days. Readings are never extrapolated backwards: hours
before every pooled account has a reading are `known: false`.

## The policy replay

- **Pool:** the mean percentage left of the group's pooled accounts. One
  confirmed subscription (a shared quota-pool fingerprint) is one supply: it
  counts once in the pool, the pace, the replay and the burn. Each enrollment
  still has its own row. Held accounts and accounts without readings stay out
  of the pool. An expired reading keeps its last used percent and is not
  pooled, so the dial does not claim current headroom; the projection may
  still assume the weekly refill.
- **Pace:** the measured burn over the last 24 h (or the measured span when it
  is shorter, at least 1 h) and over 6 h, in percentage points of one
  account's weekly quota per hour.
- **Replay:** from now, at a pace, the usable account with the soonest refill
  runs. An account is full at 100 minus its floor. A refill sets it to zero
  and moves its reset a week on. When no usable account is left, demand at
  that pace is unmet: an empty stretch until the next refill.
- **Safe pace:** the highest pace at which the pool never runs empty within
  the 7 days (a monotone search over the same replay).
- **Verdict:** too fast when the current pace leaves an empty stretch; too
  slow when the quota refilling unused is at least 10 % of the pool; otherwise
  on pace. A pace needs at least an hour of readings; before that the verdict
  is unknown.
- **Forecasts per account:** waits for refill (full now), empties the pool,
  used up while others carry on, refills with N % unused, or enough.

The server returns structured moments (`headline.at`, `until`, `focus_at`,
the refilling account); the dial words them in the person's time zone.

## States in the dial

- Too fast: "Claude lasts until Mon ~19:40, then 1 h without until … refills
  at 20:59." Too slow: "About 40 % will refill unused on Wed." On pace:
  "Enough until the next refill (Mon 20:59)."
- The default range is 3 days back (1 on phones) and at least 2 days ahead (1
  on phones), extended to the moment the headline names, at most 7. The
  Past/Ahead steppers (1–7 back, 0–7 ahead) sit behind ⋯ on phones.
- Quota not reported (Cursor, or accounts without readings): a quiet chart
  sentence and dashed rings, no curve. No accounts yet: a link to add one.
- Loading keeps skeletons at the reserved heights. A failed first read says
  "Usage couldn't be read · Try again". A failed refresh after a good read
  keeps the snapshot, shows Try again on the muted line, and ages the
  snapshot from its sampled time and each reading from its read time. A pool
  whose shown age is 2 h or more says its age; an inactive account shows its
  last reading with its age.
- The folded lines show only at 1100 px and wider and only for harnesses with
  a measured pool; they sit over the existing gap after the count, so the
  control and its units keep their positions. Amber marks a line that runs
  dry before the next refill.

## Tests

`TestQuotaOutlookReplayVerdicts` (fast, slow, on pace, refill order, held,
missing, floors) and `TestQuotaOutlookScopeAndTenant` (person only, own
accounts, tenant isolation, relinked binding) in `internal/agentaccounts`.
`TestQuotaOutlookSharedSubscriptionCountsOnce` counts one confirmed
subscription once beside an independent one. `TestQuotaOutlookExpiredReadingDoesNotInventHeadroom`
keeps an expired measurement out of current headroom.
`TestQuotaOutlookRedactionWithholdsRevokedAggregates` and
`TestQuotaOutlookRevocationBoundary` drop a revoked account's pool, forecast
and burn. `TestQuotaOutlookWebFixtureIsCurrent` pins `web/tests/fixtures/quota-outlook.json`,
which the web unit tests (`quota-outlook.unit.test.ts`) and the browser spec
(`agents-dial-usage.spec.ts`, 1440/1024/390) read, so the UI is tested
against the server's own projection. `quota-outlook-refresh.unit.test.ts`
keeps a snapshot after a failed refresh and ages it from its timestamps.
