# Routine execution consent

Automatic execution starts off. Saving an assignment, creating an occurrence,
queueing work, and supplying a lead admission adapter do not grant execution.
Ticket-only routines continue to use the existing recurrence path without
execution consent.

`GET /api/projects/{projectId}/routine-execution-settings` returns a redacted
revision, saved `consent_enabled`, effective `automatic_launch_enabled`,
`qualification_id`, and `wait_reason`. `PUT` accepts `expected_revision`,
`automatic_launch_enabled`, and `qualification_id` (null when disabling).
An authenticated interactive person with current project `harness.control` and
`run.create` must be the canonical lead-policy owner or a scope administrator.
These checks run inside the final tenant/tree-fenced transaction. Agents and
bearer credentials cannot enable or disable this person-owned setting.

Enabling requires an immutable accepted qualification for the same tenant,
project and canonical owner. It binds the current workspace/project/model
policy revisions, exact server and daemon SHA-256 artifacts, capability digest
(including adapter/model/browser pins), native host mapping, explicit native coding/browser capabilities and qualified budget
modes. Coordinator acceptance and OPS attestation are separate redacted evidence
pins. The internal `modelregistry.RecordQualificationTx` seam records them only
under existing workspace `releases.deploy` authority, held by an interactive
canonical owner/admin person. It has no routine HTTP route; routine grants
cannot write evidence or substitute their own runtime assertions.

Trusted runtime observations are supplied through `WithRoutineExecutionRuntime`
(harness) and `WithExecutionRuntime` (recurrences). They must come from the same
bounded, independently observed local/database facts and perform no network or
model calls inside a transaction. Live facts require a database-clock observation
no more than two minutes old, never future-dated. The default constructors supply none and
remain closed, including with synthetic admission adapters. S11/S26 own the
production observation integration and native qualification; this slice proves
only synthetic consent boundaries. Production stays off for release 130 until
the coordinator accepts qualification, OPS attests deployment, and a person
explicitly enables the qualified project under S27. A missing or changed
artifact, capability, policy, host mapping, budget mode, owner or observation
closes future execution immediately; saved intent is retained for explanation.

`PUT /api/recurrences/{recurrenceId}/execution-consent` is a separate interactive
owner action with `expected_revision` and `execute_consent`. It binds the saved
record, current project settings revision, exact qualification and owner
policy. Personal routines use their canonical person. Project/workspace
routines require an existing administrator-provisioned, active keyless agent
with exactly one custom role in that scope, bounded to the assignment's work
and knowledge permissions. Existing agent/role APIs provision it; consent never
creates a role, key, pairing, vendor login or secret. A project-scoped identity
cannot execute into a different output project. A different routine owner
cannot borrow the qualified lead owner's account or host policy. Unsupported
owner/target combinations remain refused pending their own qualification.

Draft preview, save, read, saved preview and final claim share the effective
policy resolver. Drafts and assignments remain unconsented. Definition edits
and pause/resume revisions require renewed consent; resume first, then consent
to the displayed revision. Scope administrators may disable a shared routine
after owner loss; personal routines remain private. The internal
`RequireExecutionConsentTx` helper re-reads the exact record/revision and current
owner under the tenant/tree fence before any new grant. Its identity result is
not an execution grant: existing dial, account, host, process, action and budget
admission remain mandatory, and each outward effect rechecks both owner and
scoped execution identity.

Disabling blocks new starts and grants without clearing reservations, unknown
process exits, billable holds, receipts or worktrees. This handler launches or
stops no process. Scoped wind-down and reconciliation belong to S19. M6 (1325)
adds only tenant/project-scoped settings and immutable qualification rows, plus
nullable opaque consent bindings on definitions. It has no dependency on later
execution/evidence tables, and DSAR inventory classifies owner and consent data.
