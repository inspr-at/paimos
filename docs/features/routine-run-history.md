<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Routine run history

`GET /api/recurrences/{recurrenceId}/runs` reads execution intent and outcomes,
including retired definitions. `GET /api/recurrences/{recurrenceId}/runs/{runId}`
adds independently paginated attempts, actions and effects. `limit` defaults to
20 and accepts 1–50. Run pages use `before`; detail pages use `attempt_before`,
`action_before` and `effect_before`. Cursors bind the resource and evidence kind
to immutable descending `(created_at, id)` order. Updates do not move records
between pages. Each request has an eight-second context and five-second SQL
timeout; usage is bounded to 200 immutable agent runs and 200 models per run.

The output project's read permission and the definition's project, personal or
workspace boundary both apply. Personal history belongs to its canonical
person; workspace history requires workspace authority. Agents retain the
existing `recurrences.manage` requirement. Tenant RLS applies to every join.
Target references, approvals and usage also check their own current source
permissions. Responses never include assignments, source events, account
identities, raw evaluation decisions, process receipts or effect payloads.
PR numbers are projected only from a recorded `pr.open` result on a readable
target. A pending external effect does not constitute a successful PR.

The closed run states are `created`, `running`, `waiting_for_person`, `held`,
`failed`, `done` and `skipped`. Every non-done state has one plain reason sentence.
New occurrence writes explicitly use `created`, meaning persisted intent with
no process launch. Older `pending`/`queued` rows map to `created`, `starting` to
`running`, `needs_person` to `waiting_for_person`, `waiting`/`blocked`/`paused`
to `held`, `ownership_lost` to `failed`, `succeeded`/`completed` to `done`, and
`cancelled` to `skipped`. Unknown values remain held. A done projection requires
attempt evidence with successful, confirmed exit; missing evidence remains held.
Go enforces the new writer and read vocabulary. No CHECK, enum or backfill is
added because previously released writers still use `pending`; a constraint
would reject them and violate the expansion policy. Preparation accepts created intent and legacy pending rows. Terminal overlap and
budget admission checks understand the new states and refuse unknown admission
states.

Usage reads share one transaction snapshot with the evidence. A run ID appears
once even if several attempts reference it. Current cumulative session/model
rows contribute once; historical usage receipts and managed telemetry are never
added again. Multiple sessions on one run cannot prove additive usage and stay
unknown. Token totals carry known and unknown model counts; truncation and
missing source permissions are explicit. Missing billing or models never become
measured zero. API list-price estimates remain separate from paid money.

`paid_microusd` comes from the existing balance's settled root grants, whose
immutable receipts already deduplicate replay and include child settlements.
No settled receipt means null, including the ledger's initial zero. Confirmed
zero is possible after a real settlement. Outstanding tokens, money and recovery
time appear separately as reserved allowance. Partial and unknown settlements
retain their holds and report incomplete coverage.

These read APIs grant no execution authority and do not change project
enablement. Automatic execution remains off until the existing person-owned
qualification and enablement flow succeeds.
