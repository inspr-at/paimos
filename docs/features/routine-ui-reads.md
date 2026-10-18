<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Routine UI reads

Routine lists and details include optional `last_run` with only `id`, `state`,
`reason`, and `created_at`. The latest visible run is selected by creation time
and ID after the existing 100-item recurrence page. The S21 states are `created`,
`running`, `waiting_for_person`, `held`, `failed`, `done`, and `skipped`. Unknown
outcomes and success without confirmed process evidence remain held. These reads
follow the definition's personal, project or workspace visibility and its output
project authority.

`GET /routine-guardrails?scope=personal|project|workspace` resolves the same hard
floor, recommendations and saved scope policies without saving a routine.
`project_id` is required only for project scope. Personal scope always uses the
caller's canonical person; workspace rules require workspace read authority.

`GET /projects/{projectId}/routine-execution-settings` adds nullable
`latest_qualification`: `id`, `accepted_at`, `host_label`, `capabilities`, and
`budget_modes`. Acceptance is the controlled record of coordinator and OPS
approval, including while the project switch is off. The host label is nullable:
the qualification stores a digest, so a display name is returned only for one
named allowed computer in the matching owner policy. Historical acceptance does
not imply that current artifacts, runtime or policy still qualify. The existing
PUT checks remain authoritative. Account selectors, computer IDs and evidence
pins are omitted.

The canonical Decision Desk adds quiet `routine_consent` pointers for interactive
canonical owners with an assignment, missing or older revision consent, current
management authority and effective execution policy. These requests are decided
through the existing consent PUT from the routine editor, using exact record
revision and a restricted execution identity for shared routines. They link to
project settings, personal settings or workspace routine settings with
`?recurrence={id}&consent=1`; the web identity prefix is `c:`. Consent, retirement
and project/policy unavailability remove the pointer. Page totals and cursors
include these sources. More than 1000 owned consent candidates returns an explicit
422 coverage error instead of incomplete exact counts. These pointers do not
produce phone pushes. The Desk runtime reader must receive the same trusted
runtime observations as execution; an unconfigured reader projects no requests.

AEON-1191 uses the S21 vocabulary and reason sentences read from
`work/aeon-1180-runhistory` at `7e18edfd2` before that branch was merged. All four
reads are additive and use existing tables; no migration or execution grant is
introduced. The approved design is AEON-1182, Routines report section d (R0).

## AEON-1191 initial verification handoff

The migration guard against `v261010123154.0.0` passed, as did all 33 ownership
inventory tests and all 314 tier/shard metadata tests. OpenAPI was regenerated
from the authored areas; their changes, route declarations, test manifests and
ownership inventory are additive. No pinned reporter response surface changed.
Remote Go verification (recurrences, decisiondesk, harness, reportercontract and
authz) was retried repeatedly but refused with exit 3 because the single night
slot was occupied. The remote static check also returned exit 3 under the night
hold. Go/contract tests, the targeted web unit file and the full static check
remain pending on the approved runner or PR CI; exit 3 is incomplete. No heavy
local fallback, origin push or deployment was performed.

## Fix round 3

The CI regression on `3d5b4ba31` failed while seeding the consent coverage
fixture: the restrictive `recurrences_definition_scope` policy rejects a marked
recurrence whose definition does not exist yet. The fixture now follows the
production create order, inserting definitions before marked recurrences in the
same transaction with the deferred foreign key. No RLS policy or production
authority was changed. The existing regression retains every assertion and now
also checks that exactly 1001 seeded candidates are visible to the owner and that
overflow returns the coverage-specific 422 before any runtime-policy work.

Current main was merged, retaining both the draft guardrails route and main's
routine-actions route. No tests or source files were added or renamed, so their
existing tier and ownership registrations remain in place. The targeted
`decision-desk-fix3.unit.test.ts` file passed all eight tests with one worker as
the single permitted small local fallback after the remote CI pool was full.
The PR's web unit execution also passed; its red job was an artifact-service
upload timeout. The policy preview requires the coordinator's trusted review
verdict; workers do not run that gate.

Remote Go/package, browser and merge-main static verification remains incomplete:
the Go runner refused the full CI pool under the night protocol, the browser
launcher is unavailable pending OPS-247, and the static runner is paused at
night. These refusals are exit 3, not green checks; the coordinator must rerun
verification on the approved runner or PR CI before merging. No origin push or
deployment was performed.
