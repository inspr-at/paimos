<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Scoped routine definitions

The recurrence API can save an optional `definition` alongside its existing
ticket template and trigger. Existing payloads retain their behavior. Saving a
scoped definition starts it paused, leaves execution consent off and starts no
agent. Adding a definition to a legacy recurrence also pauses it.

`project_id` and `parent_id` still name the **output** project and parent.
`definition.scope` independently controls the definition's visibility:

- `personal`: only the active canonical person owner can read or manage it.
- `project`: readers of `scope.project_id` can read it.
- `workspace`: authorized workspace readers can read it.

Output project visibility is required in every scope. Saving also checks both
the caller's and owner's current target/source authority in the final fenced
transaction. Project/workspace owners must be administrators of their scope.
Personal definitions cannot be created by an agent or for another person.
Private visibility also applies to audit snapshots, including snapshots from
older binaries that omit the optional fields.

`definition.assignment` saves a bounded goal, at most 32 unique live node or
knowledge references, a model registry role and active work kind, a closed
outward action list, native runtime requirements, and per-run budget intent.
The actions are `work.create`, `work.update`, `knowledge.write`, `pr.open`, and
`pipeline.request`. A saved action is subject to its eventual permission,
guardrail, review and person/pipeline gates.

Budget modes are `off`, `tokens`, `money`, and `both`. Positive integer ceilings
are limited to 10¹²; money is paid micro-USD. Off accepts no ceilings; tokens
and money accept only their own ceiling; both requires both. These fields do
not establish provider availability, reserve spending or enforce a runtime
limit. Native requirements similarly save intent without selecting a host.

PUT checks the recurrence's `expected_revision`. Scope, owner and output target
are immutable once present. An omitted or null `definition` preserves the saved
projection for old editors. An explicit complete definition without an
assignment clears the assignment. Every PUT clears execution consent.
`GET /api/recurrences?scope=project|personal|workspace` filters before the
100-row UUID page; legacy rows count as project scope.

Migration 1320 stores `recurrence_definitions`, retaining all legacy recurrence
project/parent constraints. Its nullable `recurrences.definition_scope` marker
keeps an invisible projection from appearing as a legacy row. The projection's
execution identity and person-consent fields are default off seams for consent
controls; the definition API cannot set them. Execution qualification,
guardrails, budgeting and dispatch belong to subsequent delivery slices.

Occurrences with a saved assignment now persist a pending `routine_runs` row
in the same fenced transaction as their work, inert queue entries, action
receipt and occurrence receipt. The optional `run` on a Run now response
contains its stable identity, assignment and policy-input digests and, when
queued, the existing work-order and agent-run IDs. Repeating the same occurrence
returns that identity even after the definition changes. Skipped and ticket-only
occurrences have no execution intent.

Migration 1321 adds run, attempt, typed action and effect-outbox storage with
tenant and definition-scope RLS and run-scoped receipt uniqueness. Runs freeze
the definition revision, canonical owner, output target, assignment, consent
inputs and bounded source receipt. Current owner authority is checked inside
the occurrence write fence. The policy digest represents saved inputs, not
a guardrail verdict or admission grant. PostgreSQL wakes a future executor only
after commit; a rollback leaves no work, receipt, intent or wake. The durable
outbox survives a missed notification. No attempt, process or external effect
is started here; project enablement, qualification, consent and final action
checks remain required.
