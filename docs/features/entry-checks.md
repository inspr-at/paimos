# Status entry checks

AEON-1315 implements one set of entry checks for Ready (before In progress),
QA and Done. A definition is a `definition` node at workspace scope or directly
under a project. A project copy replaces the whole workspace default, including
an empty copy. Without a definition there are no entry checks. The catalog checks
estimates, acceptance criteria, an approved attached design for UI work, a named
blocker, another build in flight, CI against latest observed main, independent
review, visual approval, release copy, merge and release facts. Conditions can
restrict a check by kind and labels. Unknown or duplicate rules fail closed.

`GET /api/nodes/{id}/entry-checks` returns current checks, the definition source,
immutable entry records and the latest agent refusal. Status writes and queue
admission evaluate the same definition under the tenant/tree fence and current
write authority. Agents receive HTTP 409 `entry_checks_failed` with `to_status`
and the failing checks. A person with status-write permission can supply a
nonblank `override_reason` (at most 500 characters). The status change, missing
checks, definition revision, person and reason are one audited event. Identical
consecutive agent refusals fold in the activity display without rewriting audit
history. Release text is the `release_copy_set` check; hidden release notes and
content work are exempt. Structural pill validation remains in place.

Definitions are read and saved through `/api/projects/{id}/definitions` and
`/api/settings/definitions`. Writes require a person project admin or workspace
`settings.manage`, respectively, and the current `expected_revision` (null for
creation). `copied_from_revision` pins the workspace revision and an ordinary
`cites` relation records the copy source. Events retain previous definitions for
the later settings editor. Generic node writes cannot change definition nodes.
No new tables, dependencies or permissions are introduced.

Manual items use `manual:<slug>`, a label and optionally `owner_id`. A named
person with current project access is preferred; otherwise the person assignee
or a current project admin owns the item. Only that owner or a project admin may
tick or undo it through `PUT /api/nodes/{id}/entry-checks/{checkId}`, with a ticket
revision precondition. Completion provenance belongs to an event bound to the
definition revision. Changing the definition requires checks to be confirmed
again. Decision Desk routing, reassignment controls and the existing HumanCheck
owner treatment belong to P4.

`POST /api/nodes/{id}/entry-evidence` records design/visual approval (a person
project admin) or an external coordinator's independent review verdict. Review
records require current coordinator authority or a person project admin, exact
head/base SHAs in the project's linked delivery observation, and distinct
recorded author/reviewer sessions with known, different provider families.
Reviewer sessions use the existing worker session role. Families are derived
from PAIMOS session metadata, not supplied in the request. A changed range or
later negative verdict invalidates the observation. This record does not grant
merge, deployment or release permission. `reviewgate.CoordinatorOKTx` exposes
the shared read predicate for other delivery consumers.

The reserved ticket row opens one checklist for all three statuses. Missing
checks offer the existing estimate, criteria, blocker and release-text fixes;
status refusals open its reason form. Completed tickets show their entry record
in a read-only panel. Status menu counts show the current entry record and later
statuses. Project and workspace settings editors are delivered by P2 and P3.

The status menu opens the checklist from its current ticket revision before
changing a row with failing checks. Release-text fixes and the ticket editor
identify incomplete fields when the definition requires them; hidden and
no-release work remain exempt. Once the checks pass, the move needs no override
reason. The CLI prints the failed check ids, labels and reasons, with bounded
output. Queue rows retain provisional missing-field advice until the server
returns readiness for that revision, including an empty definition.

Bulk status writes acquire the complete builder-fence batch in sorted ticket-id
order before locking node rows. Entry checks can then re-enter those fences
without acquiring another builder lock after a per-ticket audit event.
