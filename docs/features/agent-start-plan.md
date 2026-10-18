# Agent start plan

`aeon agents plan` shows the person's planned total, per-harness limits and
running counts; `--json` returns the same snapshot as `GET /api/agents/plan`.
The person reads their own plan. An agent key reads only its live person
creator's plan and needs the explicit `agents.plan.read` scope and live role
permission. There is no person or tenant selector. Existing coordinator keys
must receive this scope through the person's normal key-scope controls.
Plan reads, writes and running counts use the canonical person, including when
the caller or key creator is a linked alias. The snapshot returns that canonical
`principal_id`. With no canonical preference, a saved alias preference remains
readable; conflicting effective plans across linked rows fail closed. An
explicit person save updates the canonical row and reconciles existing alias
copies. Other preferences remain private to the calling principal.

The person saves the plan through `PUT /api/preferences/agents.working`:
`{"value":{"total":5,"limits":{"codex":4,"cursor":"off","claude":"no_limit"}},"expected_updated_at":null}`.
`expected_updated_at` is the exact timestamp from the last plan snapshot;
null requires an unset plan. The check and save are atomic across linked aliases.
A stale revision returns 409 without writing; re-read before another change.
Legacy clients may omit this optional precondition. The dial always supplies it,
discards queued stale edits on conflict, and shows the re-read values for review.
Total and numeric limits range from 0 to 30. Missing limits mean No limit;
numeric zero means at most zero, while `"off"` explicitly disables a harness.
Limits may add up to more than the total. Legacy `cap` (1–12) maps to total;
legacy area/model reservations are ignored, because reservations cannot become
maximum limits. Legacy values keep their original shape for existing UI
clients. No stored total defaults to 15, the existing launcher ceiling.

Running counts include unended, unarchived sessions owned by the person across
projects, including starting, waiting, idle, throttled and stopping sessions.
Silence alone does not release a slot. Ownerless legacy sessions count only
when their agent's key creators identify one person unambiguously. The API
returns counts without session or project details. Malformed saved plans fail
closed. `agentplan.CanStart` checks total and harness limits without changing
running work; launchers must serialize starts and check account room separately.
On **Agents**, the control reads “Run up to … agents at once.” The total and
harness limits save to the same canonical plan used by coordinators. The folded
line starts with the agent mark and keeps the same raised round − / + controls
as the expanded rows. Harness values follow Off → 1…30 → ∞ (no own limit):
− from 1 selects Off, + from 30 selects no own limit, and − from ∞ sets a ceiling
one below the harness's effective total/account limit. Buttons stay visible and
disabled at the ends. Clicking the harness mark cycles No limit → At most → Off;
the tooltip names the next mode. The ∞ value's tooltip includes the effective limit.
A stored API/CLI zero stays visible and only + is enabled. Details grow below the
dial with each harness's controls and the
read-only Now, Accounts, Waiting and Checks. Folding is remembered separately
for the signed-in viewer in `agents.working.display`; it never changes the plan.
Click any total or harness value to type; Enter or blur applies, and Esc cancels.
An empty harness field selects no own limit, 0 selects Off, and positive whole
numbers clamp to 30. The total accepts whole numbers from 0 to 30; invalid or
empty total drafts leave it unchanged. Drafts are discarded if the viewer, target
value or plan revision changes. Editing occupies the value's original slot.
Arrow keys step a focused stepper or move between harness modes; Home selects
Off and End selects no own limit on a harness stepper. Browser and OS modifier
shortcuts stay native. Failed saves stay visible across successful
polls until another deliberate change. Repeated selections and arrows at a
boundary do not write. The live line announces total changes once.
Unknown account or queue readings stay explicit. The visible queue's ready work
is not reported as starting until the launcher picks it up. Lowering the total
or turning a harness off never pauses or stops existing work. Launcher
integration remains a separate AEON-540 part. The approved start-check copy
requires part D to read `/api/agents/plan` before every start and enforce the
total, harness limit and account-room checks before this UI is released; keep
AEON-540's pill and benefit out of release notes until enforcement is live.
