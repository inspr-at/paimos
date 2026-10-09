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

The start gate now shares this account projection in the final reservation and
claim transaction. Shadow admission populates the same daily snapshot before
its decision; Models uses the saved ranked order for qualified fallback. See
[the start-gate contract](agents-start-gate.md) for refusal and guard behavior.
Running work is unchanged. `next_on_ladder` is null until Models resolves the work-specific
ladder; reset credits and reset plans are null until the resets package supplies
vendor-backed values. `daily_reset_policy` is suggest when unset. This package
adds no vendor reset action, settings controls, or dial UI (the dial is AEON-1036, below).

Validation of source commit `4f05c307d`: the daily behavior tests in agentplan,
agentaccounts, views and db passed on the approved remote runner, including
local-day/DST maths, account privacy/delegation, writes/409 and migration
rollback/RLS. The full reportercontract package passed after generating the
OpenAPI bundle. The final remote `ci-static --merge-main` exited 0 with all
40 checks passed and no skips. The wider Go run also passed DSAR, engine
admission and delivery. Coordinator review and release gates remain separate.

## The dial (AEON-1036)

The Agents page dial shows and edits these limits (approved AEON-1030 draft 6).
Folded, it is one row as high as the other folded cards: the sentence, one chip
per harness (mark, stepper and, only when needed, a gold flag for over pace or
a red flag for today's limit) and `● N running (M waiting) ▾`. Everything else
about a chip, such as `Codex · at most 6 · ≤ 68 % used today · on pace`, is
hover and keyboard-focus text. A flag, or on a phone a chip's icon with its
count, opens the dial at that harness. The toggle opens one info area with three
tiles: running and waiting against the dial, the accounts per harness with the
existing Verify action, and the checks before every start.

Open, the dial is master and detail. Fixed 88 px rows on the left carry the usage
lines and the pace state; the detail on the right reads week used and left,
today's points with a teal, gold and red bar, resets only where the vendor
reports them, then the folds Pace and Boost today, the choice at the limit
(follow the model ladder, or wait) and the accounts read-only. Pace and the
choice at the limit write `daily` through `PUT /api/preferences/agents.working`
with the whole current `daily` map and the plan's revision; the total and
limits are never changed by such a write, and a plain total or limit change
never sends `daily`. Boost today is typed as used or left, stored as the used
percentage and ends at `daily_until`. It is read-only without `account.manage`
or for accounts the person cannot change. This supersedes the header Boost today
of AEON-883; `BoostToday.vue` and its web helper are removed.

The dial never claims more than the server measured: the server also reports
`at_limit` when a reading is merely stale, which the dial words as "usage not
measured right now" without a flag; a harness without an account or a reading
says so. The selected harness (the last pick, else one over pace or at its limit,
else the first with a daily limit), the info area and the open folds are
remembered per person in the preference `ui.agents.dial`. Resets, the Resets
card, the posture control in Settings and the default pace editor belong to other
packages; the dial links to Settings › Accounts and computers for them.

