# Daily limits before agent starts

AEON-1034 uses the AEON-1033 daily plan for new managed reservations and
launch claims. The account's current canonical owner, linked plan, local day,
floor, legacy posture/boost compatibility and precise vendor reading are read
again inside the existing fenced transaction. A prior reservation, Run now or
recovery permit does not bypass a daily ceiling. Already running work keeps
its existing replay and settlement behavior. Pairing verification retains its
separate, bounded verification path.

Measured usage at or above the account's `limit_used_pct` refuses with
`daily_limit`. The daily percentage is a weekly or monthly vendor window.
A session window, such as five hours, stays on the existing quota gate and
is not a daily ceiling. A sample older than the account's current link
belongs to the previous binding, so it is not this door's percentage. A door
whose `freshness` is `unknown` and which has no `used_pct`, `limit_used_pct`,
`start_of_day_used_pct`, `read_at` or `resets_at` has no percentage ceiling.
It is not `no_daily_limit` (that flag is API billing only), it does not invent
dial headroom, and it does not prove exhaustion for a model switch. A zero-value
door or any partial measurement is not this shape. An account with no owner has no person's daily plan, so this gate adds no
ceiling and quota admission is unchanged. A privacy-withheld projection,
including one hidden because a private sibling shares a resource, is not a
usable percentage: it adds no ceiling and does not prove exhaustion. An
unreadable or malformed plan, a long window without its precise percent, a
post-link window with an absent baseline, an expired reset, a future reading
or a reading older than `ProbeFreshness` (two minutes) refuses with
`daily_limit_unknown`. A redacted door in the plan snapshot still keeps that
harness unknown for model selection.
API-billed doors have no percentage-based daily ceiling. Daily waits use the
canonical person's next local midnight, with calendar/DST arithmetic.

`GET /api/models/resolve` retains Settings → Models order and shared successor
qualification. With `at_limit: ladder`, an exhausted selected harness is
excluded and the same work-specific resolver is rerun. The successor still
needs an allowed profile, available account, project fence, residency and
existing review qualifications. Only owned doors with daily room appear in
the selected qualifying account IDs. `wait` and unknown daily evidence return
no selected profile; `trace.blocked` is `daily_limit` or
`daily_limit_unknown`. An explicit harness filter remains binding. Catalog
previews before any accounts are enrolled remain advisory; they grant no start
authority. No model or key scopes are changed.

`POST /api/engine/admission` remains shadow-only and returns `enforced: false`.
It reads the same daily state in the final audited decision transaction.
Both ladder and wait refuse the exhausted requested harness. The caller
resolves the successor separately, then evaluates that harness with a new
request ID. Replay returns the original decision; it is not fresh authority.

LEAD guard migration (AEON-865): `plan-gate.py` and `quota-guard.py` use
`GET /api/agents/plan` with the existing `agents.plan.read` scope, from the
selected personal PPM account. Request time and response size must be bounded;
an HTTP/parse/shape failure means no new starts. Do not cache an allow across a
start or substitute a local percentage constant. The required projection is
`total`, `limits`, `running`, `daily`, `daily_state` and `daily_until`.

For the requested harness, inspect every account door, not just the display
state or active account. Use the server's absolute `limit_used_pct`; do not
recompute Boost, floors or midnight locally. Session windows are not daily
percentages. A door with `freshness: unknown` and no `used_pct`,
`limit_used_pct`, `start_of_day_used_pct`, `read_at` or `resets_at` has no
percentage ceiling. Every other door requires
`freshness: fresh`, `read_at` no later than now and no older than two minutes,
`resets_at` after now, and numeric `used_pct` and `limit_used_pct` in 0–100.
A partial, stale, future, expired or redacted door does not prove headroom or
measured exhaustion, and one such door keeps the harness unknown. A fresh door
below its limit provides daily room. An account with `no_daily_limit: true` is a visible API-billed door with no
percentage ceiling; absence means false. A whole-harness `no_limit` state also
denotes visible API-billed doors. This discriminant remains explicit when
subscription and API doors share a harness. Once the day ends, fetch a
new plan. Used and left are display pairs, not two independent limits.

When all eligible doors are measured exhausted, `daily[harness].at_limit`
selects `ladder` or `wait`. Ladder calls
`GET /api/models/resolve?ticket=<work-key>&role=<work-role>&situation=<round>`
with the existing Models read authority and the work's author family for
review. No caller-selected provider order or unqualified successor is allowed.
A null profile refuses; a selected profile still passes the existing count,
account and host gates and the server's final reservation/claim check. Wait
refuses until `daily_until`; an unknown plan retries a fresh read and never
switches models based on stale usage. First-build finishing reserve and PR WIP
remain separate existing gates.

`claude-guard.sh` is retired by LEAD only after this contract ships and the
plan readers are switched. This repository change documents that handoff; it
does not edit coordinator-owned scripts, deploy, or enable the shadow gate.
No migration, version change, vendor reset action or dial UI is included.
