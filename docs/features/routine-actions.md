# Routine work and knowledge actions

The routine action broker accepts `work.create`, `work.update`,
`knowledge.write`, `pr.open` and `pipeline.request` at
`POST /api/routine-runs/{runId}/actions`. Existing manual
work and knowledge endpoints continue to use their ordinary rules.

An action carries the running attempt ID, a run-scoped action key, the output
project, the target ID and its `updated_at` expressed as integral Unix
microseconds. Creation targets a parent; knowledge creation targets the output
project. Work targets must remain inside the run's frozen output subtree.
Knowledge updates keep their type and slug and cannot edit the assignment's
source entries. Knowledge creation records a proposed entry. Work status
updates refuse parent status even while the legacy parent-status feature is off,
and retain the ordinary leaf and benefit checks; queued terminal changes
must pass through the existing pipeline.

Requests are limited to 64 KiB, titles to 500 UTF-8 bytes and bodies to 32 KiB.
The existing endpoint body guard bounds transport reads before database admission
under a ten-second request deadline, and interrupts a cancelled or stalled body.
Unknown fields, mixed payloads, arbitrary paths, recipients, commands, surveys,
merge and deploy actions are rejected. Administrative pause notices belong to
separate system evidence. PR and pipeline requests persist their typed payloads
in the existing effect outbox before any external effect.

The final transaction takes the tenant, tree and pairing fences, reads the
persisted running run/attempt grant, and checks current caller and owner rights,
definition revision, assignment, consent, execution identity and exact project
qualification. It requires a verified guardrail allow bound to the current
policy and action context. The frozen assignment must still match its immutable
digest and the current definition. Deterministic denials cannot be overridden by
the independent evaluator. The S08 evaluator supplies already persisted evidence
through `WithActionEvaluator`, including the persisted evaluation ID. The broker
checks that allow record against the current definition, policy, context and
hard/preset versions, and links it in the immutable action receipt. No model or
network call runs inside the write.
After qualification and consent pass, an absent verifier returns
`guardrail_evaluation_required` and writes no effect or success receipt.
Project execution remains default off.

Each successful effect and its immutable request digest/receipt commit in one
transaction. Audit events retain owner attribution and are appended after
remaining writes.
Run/action identifiers appear in public events only when definition and output
share the same project visibility; private lineage stays in scoped receipts.
Identical authorized replay returns the original receipt; different content
with the same key conflicts. Failed
writes roll back their receipts. A retry must still have current run authority.

Tests exercise mutation/receipt replay and rollback below the admission boundary.
External-effect tests use synthetic evaluator fixtures through the real verifier
and persisted decision latch; they claim no native qualification. Route and
barrier tests prove that pending runs, absent evaluation and owner revocation
cannot produce an effect. Native integration and activation remain subject to
the coordinator and OPS qualification.

`pr.open` freezes the same repository, branch, main base, base/head SHA, title
and body with a deterministic operation reference. The approved GitHub App
receives a short-lived token limited to that repository and PR writes; missing
App access or permission stays pending. It cannot merge, enqueue, publish or
deploy. The operation marker in the PR body permits bounded reconciliation
after response loss. A durable send claim permits one POST only. A crash after
that claim but before the request leaves an honest uncertainty for a person;
automatic recovery reads the original reference and never posts another PR.

`pipeline.request` references the PR action and exact artifact head. Once the
coordinator has completed the review lineage, recovery rechecks current review,
required CI, tested source SHA and owner rights. The existing approval source
and Decision Desk carry a link to the routine run and PR. Its exact digest
includes the review, guardrails, required checks, artifact and queue SHA; a
changed binding requires another person decision. Approval tracks the normal
pipeline and gives the routine no merge, publication or deployment permission.
A synthetic merge-queue SHA needs its own required checks and tested artifact.
OPS supplies the qualified artifact reader; absence holds the request.

A merged PR completes its routine only when the exact AEON-1008 merge-to-Done
receipt is visible. Responsible-person fallback and missing-owner/benefit
refusals remain owned by that existing automation. The routine broker never
writes work status directly. Non-code results use an explicit result digest,
confirmed process exits, settled usage, completed catalogue actions and an exact
person approval; they do not fabricate a PR. Every installation remains inert
until its separately qualified, person-owned project execution flag is enabled.
