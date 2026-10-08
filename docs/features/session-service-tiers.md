# Session service tiers (AEON-436)

`aeon agents tier show --project KEY --session UUID` shows Default / Fast / Fastest,
per-model adapter provenance, published price multipliers and pending decisions.
`set --tier fast` uses a person key with the existing project `harness.control`
permission. It queues a change bound to the current daemon/process generation;
the active tier changes only after the daemon acknowledges the native mechanism
at an idle boundary. Codex resumes its owned thread with `service_tier` for the
next run; Claude applies its `fastMode` setting for the next turn. A rejected or
uncertain native acknowledgement leaves the active tier unchanged. A session
running on its own and an ended session are read-only.

`ask --tier fast --reason "QA waits"` records the session agent's request without
exposing its daemon lease or switching the tier. Persons can use `approve` or
`decline --decision-request UUID --tier fast`; the tier API exposes requests for
the Decision Desk. Undo uses `set` with the previous tier: it cancels an unclaimed
change or queues a reversing change after confirmation. A claimed change must
settle before a reversing change can be queued.

The session-bound `/tier/report` endpoint carries the adapter's facts for each
model: price, speed (unknown where unpublished), mechanism, harness/adapter
version and checked-at. Offered tiers and all reported multipliers must match the
pinned vendor catalog; reports cannot supply their own price or usage factors.
The currently named Codex and Claude models have no cited tier price pins, so
Fast and Fastest are not offered and their multipliers remain null. A paid tier
requires a cited vendor price and mechanism before it can be enabled. Price and
subscription capacity consumption are distinct; the fragment's illustrative factors are not
pricing defaults. Vendor facts are pinned in `internal/servicetier`; account or
vendor rejection remains authoritative. Usage snapshots retain tier segments and
their multipliers, and run telemetry records the active tier. Earlier tokens are
never repriced when a tier changes; vendor billed costs remain vendor costs.
On Agents, the Tier column uses one small chevron per offered tier. Clicking the
glyph, or Enter/Space, opens Change tier; plain Left/Right and the hover minus/plus
step among offered tiers. The eight-second Undo toast cancels an unclaimed change
or reverses the confirmed change, and refuses an intervening revision or process
replacement. The session menu and Service tier block open the same picker;
phones use a full-height sheet with pinned actions. Agent requests can be approved
or declined in the session panel; Decision Desk integration remains AEON-536.
Unavailable tiers show their adapter reason, including “not offered: no published
price”. Unknown prices and model-time baselines remain explicit rather than
producing last-run estimates from the design fragment's sample numbers.

The tier API also returns `last_change`, including the completed control's outcome
and reason. A rejected, expired or superseded change reports an error and clears
its Undo receipt. Cancelling an unclaimed change is neutral: its successful Undo
receipt uses `tier_cancelled_pending`; a cancelled control uses `tier_cancelled`.
Neither shows a rejection alert. Other viewers following the change get a neutral
cancellation message. A real rejection can be dismissed for the current viewer;
starting the next change also retires it, and a later rejection still appears.
Overlapping tier reads share one result. Confirmation reads
back off from 1.2 seconds to 30 seconds and stop after two minutes; they stop
while the tab is hidden or Agents is closed. Live tier revisions and control
completion events still reconcile the result after that limit, and Check result
allows an explicit read. Heartbeats alone do not reload the tier panel.
Pending agent requests are carried by the session list row, so its “asks for”
hint does not depend on opening the panel. Host controls remain visible in the
compact layout alongside the tier control. The session list uses cards below
760 px container width, including a 1280 px viewport with the panel open;
1366 px and 1440 px panel-open viewports retain the table.

AEON-612 adds frozen token-estimate labels (for example `Fast ×2 · $2.40 at Default`), independent of the session's current tier. Mixed tiers name each
frozen multiplier. This is an estimate, never vendor billed cost. The tier API's
bounded `history` records requests, approvals, declines, confirmations and both
forms of Undo with the person's identity and agent attribution. Cancelling an
approval preserves its history even when the request becomes pending again.
Reads show the latest 50 tier decisions and explicitly report truncation;
older decisions remain stored. Unconfirmed daemon outcomes remain unconfirmed
in the history instead of asserting that a switch was applied or reversed.
Tier decisions and daemon claim/completion batches write history before taking
the tenant event counter. Audit failure rolls the entire decision back.

`estimates` compares tiers against one last completed run with the same project,
agent, harness, model and effort, with final tokens and a frozen price version.
Its source names the run, sample count (0 or 1), tokens, actual cost, total time,
measured model time (or its absence), the frozen sample tier and price version.
The sample's single measured tier shows actual figures without “≈”; other tiers
remain projections. Unread or failed evidence shows a quiet placeholder with no
sample basis, rather than inventing a zero-run result. Telemetry and usage wakes
refresh only the selected session/run, at most every five seconds with one
trailing refresh; tier decisions still wake confirmation immediately. The sample
identity lookup uses the partial index in migration 1134. Unpriced tiers stay
unavailable; absent priced samples say “no estimate yet”. Optional cumulative
`model_time_ms` usage measurements and frozen segment speed factors permit time
comparisons: only model time scales; tools and waits retain their duration.
`active_ms` is never substituted for model time. Cost estimates can be available
while time remains unknown. Legacy usage without frozen tiers is not backfilled
from today's catalog. Mixed-model sessions are excluded from whole-run estimates.
An Undo that also accepts an agent's fresh request records both approval and Undo.

DSAR handoff (AEON-490): `internal/dsar/inventory.json` is absent from this stacked
branch and the cached `origin/main`. At integration, classify migration 1134's
`harness_tier_history.actor_id`, `session_id`, `request_id`, `control_id`,
`undo_of_control_id`, `action`, `from_tier`, `to_tier` and `created_at` as personal
(attributed decisions and pseudonymous linkage); classify `tenant_id` and `id`
as metadata locators. Locate rows by `(tenant_id, id)` and link the affected
principal through `actor_id` or the tenant-scoped session/request/control joins.
Actor names and asking-agent names in the response are personal data inherited
from those principal joins. Classify `harness_session_usage.model_time_ms` as
metadata (reported performance measurement), located by
`(tenant_id, session_id, model)` through the tenant-scoped session. No new column
is a secret. Carry these classifications into the inventory in this same PR if
AEON-490 lands before integration; this note does not replace that guard.

Browser evidence remains pending OPS-247: the 1440/390 × light/dark picker guards,
delayed-evidence control checks and zero-run/error cases must run in the approved
browser lane or CI-equivalent Linux Chromium before release. They are not a
passing browser gate merely because the specs exist.

Regression coverage includes native acknowledgement and Undo, pending replay,
tenant/project isolation, revoked authority, vendor cooldown and stored costs
across tier changes. Requests that leave the active tier unchanged also retain
their idempotency receipt; reusing their ID with a different body returns 409.
