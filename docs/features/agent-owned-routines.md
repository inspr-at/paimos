# Agent-owned routines

A project Routine can name an active agent principal as its owner. The existing
canonical person in the definition remains accountable for the grant. This uses
the existing recurrence calendar, occurrence receipts, run history and effect
outbox; it creates no assistant identity, key, role, schedule engine or UI.

Create an ordinary project-scoped recurrence in its output project, without a
native assignment or `queue_each`. Its canonical owner grants agent ownership
with `PUT /api/recurrences/{id}/agent-owner`, supplying `expected_revision`,
`owner_principal_id` and `destination`. The grant requires an interactive person
session and live recurrence, inbox and output permissions for the person and
agent. It pauses the routine; the existing resume control activates its schedule.
Changing the routine's input invalidates the grant until renewed. Pause/resume
controls preserve an otherwise unchanged grant. Personal/workspace routines and
native assignments retain their existing ownership and execution consent rules.

The destination is `ticket_comment` with a same-project `ticket_id`, `new_ticket`
under the routine output parent, or `knowledge`. Every occurrence retains its
ordinary tracking ticket and creates one run. The scheduler reconciles a separate
`routine.agent_dispatch` intent through the existing principal inbox. A configured
primary messaging target uses its existing adapter, including `grok_bot_routine`
and local agentd targets. An owner without a target receives ordinary principal
inbox work for an existing listening worker. Acceptance is queueing evidence;
it does not prove process execution or start a new builder process. No provider
calls are made by this module.

Before each inbox acceptance, `Module.WithAgentBudget` must explicitly allow the
run. Its callback receives the transaction, tenant, owner, run ID and UTC Monday
week boundaries. It must atomically check/reserve the owner's weekly allowance,
including admitted runs and unknown costs. Missing policy holds the run with
`budget_policy_unavailable`; refusal holds it with
`owner_weekly_budget_exhausted`. The next scheduler pass retries budget holds.
AEON-754 package F supplies the package A policy integration. Production dispatch
therefore remains closed until that integration is installed.

The server wires `routineactions.WriteAgentResultTx` through
`Module.WithAgentResultWriter`, sharing the ordinary result mutation guards.
The dispatched owner submits `status`, `title`, `body` and optional integral
`cost_microusd` to
`POST /api/recurrences/{id}/runs/{run}/agent-result`. Only `succeeded` and `failed`
are accepted. Successful results use the ordinary ticket, append-only comment or
proposed Knowledge write guards. Failed results retain bounded diagnostics in
the existing effect ledger and create no success output. Exact retries return the
same receipt; a different result conflicts. Final writes recheck the live person
grant, definition digest, owner, key scopes and destination under the access/tree
fence. No result accepts Knowledge, completes a tracking ticket, grants an outward
action, merges code or deploys anything.

`GET /recurrences/{id}/runs` retains the ordinary status logbook.
`GET /recurrences/{id}/runs/{run}/agent-result` returns the result node and optional
agent-reported cost. Missing cost stays unknown; it is never presented as measured
billing or a native process receipt. Run success means the result was stored.

Tenant and project RLS already protecting definitions and runs also protect the
nullable ownership/result extensions. The agent-owned execution path is excluded
from the native executor's dispatch/recovery scan. Existing messaging-only
restrictions remain effective; policy wiring must not treat ownership as a way
to bypass them.
