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
