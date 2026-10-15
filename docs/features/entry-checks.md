# Status entry checks

AEON-1315 implements one set of entry checks for Ready (before In progress),
QA and Done. A definition is a `definition` node at workspace scope or directly
under a project. A project copy replaces the whole workspace default, including
an empty copy. Until either scope has a saved definition, a built-in workspace
default requires an estimate, acceptance criteria and a named blocker (for
blocked tickets) before In progress, and release text and `lesson_recorded`
before Done. A saved empty workspace or project definition deliberately allows
free movement.
The catalog checks
estimates, acceptance criteria, an approved attached design for UI work, a named
blocker, another build in flight, CI against latest observed main, independent
review, visual approval, release copy, merge and release facts. Conditions can
restrict a check by kind and labels. Unknown or duplicate rules fail closed.

`lesson_recorded` applies only before Done to tickets tagged or labelled
`incident`, `regression` or `non-obvious-fix` (also `non-obvious fix` and
`non_obvious_fix`), or marked with `fields.non_obvious_fix: true`. It passes
when an accepted ticket or comment learning links a changelog line that remains
in a live Knowledge entry in the ticket's current project, or an explicit
`No lesson: <reason>` line is present in its body, description or a current
comment. Recommendations, dismissed learnings, blank reasons and deleted or
edited-away notes do not count. Agents receive the standard refusal; people
can override with a reason recorded in the status entry. Saved definitions
retain their whole-copy replacement semantics. Body and status changes, including
Undo, evaluate the body that the mutation will write.

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

The built-in default uses the all-zero UUID and a stable policy revision in
responses and entry records; it is not a stored node. Saving the first definition
still uses `expected_revision: null`. Loading never writes a definition node.

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
Loading alone does not insert a row when no definition is known. Revision
refreshes retain a known definition's row, avoiding transient layout shifts.

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

Project settings › Definitions (AEON-1316) follows Repository & delivery. It
shows the inherited workspace default read-only until a person project admin
chooses “Change for this project”. Every edit saves the whole project copy;
removing every check deliberately allows tickets to move freely. Rows explain
added checks, changed conditions and checks left out of the default. More than
three left-out checks fold below the three status blocks. Remove leaves a row
with Undo; restoring a folded check keeps that row in place for the visit.

The shared editor adds catalog checks and person-confirmed free-text items,
reorders checks within a status, and restricts them by ticket kind and required
labels. A manual item can use the person assignee (falling back to a current
project admin) or a named person from the project's people. Server authority
still decides whether that person has access. Failed saves retain the current
checks and drafts. Late responses are discarded after project, person or tenant
changes. Read-only callers see the checks and a lock explanation.

`GET /api/projects/{id}/definitions/context` includes current project edit
authority, the effective definition, the current workspace default and the
server-retained checks from the copied revision. Ordinary edits preserve that
base even after the workspace default changes. `DELETE` on the definitions
resource resets to the current default; both `expected_revision` and
`expected_workspace_revision` must match. Reset confirms the checks and
conditions that will change, writes one `definition.reset` event, and preserves
ticket statuses and entry history. The existing built-in workspace policy stays
in effect when no saved workspace definition exists. P3 owns workspace editing
and the decision controls for later changes; P4 owns Desk routing and per-ticket
owner reassignment.
