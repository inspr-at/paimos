# Daily limits in the agents plan

AEON-1033 adds the data for the approved AEON-1030 draft 6 dial. The existing
`PUT /api/preferences/agents.working` accepts optional `daily` settings keyed by
harness: pace (1–50 percentage points a day, or the person's default), use
everything, an absolute Boost today used percentage, and ladder or wait at the
limit. A number entered as left is converted to used before saving. Boost may
raise or lower the ceiling and expires at the person's next local midnight.
The existing atomic `expected_updated_at` precondition and 409 are unchanged.
Older clients that omit `daily` preserve existing daily controls.

`GET /api/agents/plan` returns effective daily settings and `daily_state`, with
ordered account readings, used and left together, first local-day usage, floor,
limit, reset timestamp, and freshness. The first measured reading after the
canonical person's IANA midnight is the baseline, not the latest plan read.
Calendar arithmetic handles daylight saving days. Without a saved personal
timezone the day uses UTC; the default pace is 10 percentage points a day.
The nullable `personal_profiles.daily_points_per_day` column is the Settings
package's storage surface; a stored value outside 1–50 fails closed.

The ceiling is the smaller of `100 - floor` and either the active absolute
boost, 100 in everything mode, or the baseline plus the daily pace. Over pace
means consumption above the pace, even when a boost permits further use. The
harness reaches its limit when all its account doors are exhausted. Usage older
than the existing two-minute ProbeFreshness has no fresh daily headroom. Unknown
or privacy-withheld usage has null numbers and cannot imply zero use or an
unlimited account. API-billed accounts have no percentage-based daily limit.

Account data uses the existing overview windows, routing order, shared-quota
floors and privacy boundary. The plan-read scope delegates the creator's own
canonical account family; it does not grant the separately scoped tenant-wide
overview. Agents cannot write daily settings. A boost edit additionally checks
live account.manage and quota visibility inside the fenced preference write.

Until a harness receives explicit daily settings, existing per-account posture
is projected as careful = pace 5, balanced = the default, maxout = everything.
Active legacy +N boosts become baseline + pace + N through midnight, bounded
by the floor. Accounts with different legacy policies retain their own limits;
the harness settings display the first account's effective policy. Explicit
daily settings supersede those legacy values for the owner's harness. Existing
posture/boost routes and stored columns remain for backwards compatibility.

Migration 1302 is additive: nullable defaults/reset policy, an indexed first
reading lookup, and immutable readiness-observation history with tenant RLS.
Older readiness writers populate history through an additive trigger; no
existing values are rewritten. Rollback runs the older binary and retains the
expansion and history. All new storage is classified in the DSAR inventory.

This is plan data groundwork. The start-gate package must call
`agentaccounts.ReadPlanTx` (or populate the same snapshot within its final
transaction) before implementing daily refusal and model fallback. Running work
is unchanged. `next_on_ladder` is null until Models resolves the work-specific
ladder; reset credits and reset plans are null until the resets package supplies
vendor-backed values. `daily_reset_policy` is suggest when unset. This package
adds no vendor reset action, settings controls, or dial UI.
