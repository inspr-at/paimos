# Routine work and knowledge actions

The routine action broker accepts `work.create`, `work.update` and
`knowledge.write` at `POST /api/routine-runs/{runId}/actions`. Existing manual
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
Unknown fields, mixed payloads, arbitrary paths, recipients, commands, surveys,
merge and deploy actions are rejected. Administrative pause notices belong to
separate system evidence. PR and pipeline effects belong to their own persisted
outbox and reconciliation brokers, rather than this internal-action route.

The final transaction takes the tenant, tree and pairing fences, reads the
persisted running run/attempt grant, and checks current caller and owner rights,
definition revision, assignment, consent, execution identity and exact project
qualification. It requires a verified guardrail allow bound to the current
policy and action context. The S08 evaluator supplies already persisted evidence
through `WithActionEvaluator`, including the persisted evaluation ID. The broker
checks that allow record against the current definition, policy, context and
hard/preset versions, and links it in the immutable action receipt. No model or
network call runs inside the write.
Without that verifier the route returns `guardrail_evaluation_required` and
writes no effect or success receipt. Project execution remains default off.

Each successful effect and its immutable request digest/receipt commit in one
transaction. Audit events retain owner attribution and are appended after remaining writes.
Run/action identifiers appear in public events only when definition and output
share the same project visibility; private lineage stays in scoped receipts. Identical authorized replay returns
the original receipt; different content with the same key conflicts. Failed
writes roll back their receipts. A retry must still have current run authority.

Tests exercise mutation/receipt replay and rollback below the admission boundary
without fabricating independent evaluation evidence. Separate route tests prove
that pending runs, absent evaluation and barrier-controlled owner revocation
cannot produce an effect. Native integration and activation remain subject to
the coordinator and OPS qualification.
