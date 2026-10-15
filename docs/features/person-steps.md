# Owned person steps

Definition manual items and the per-ticket human check have one current person
owner. A manual item uses a named owner (`owner_id`) or the person assignee,
then its named fallback (`fallback_owner_id`), then the first current project or
workspace admin. A ticket human check uses the person assignee, then an admin.
A per-ticket reassignment overrides that policy for that step and definition
revision. An explicit owner who loses access falls back to an admin immediately.
Agents and deactivated people cannot own or tick a step.

The ticket checklist and human-check box show `Who: …` and let the owner or a
project admin ask another current person. Only those people with `nodes.write`
may tick or undo. The existing ticket PATCH route and causal Undo enforce the
same live authority under the tenant/tree fence. A person may still move past
failing entry checks with an explicit audited override reason; an agent's
refusal names the person it is waiting for.

The owner gets an Action request in the Decision Desk when a manual item's
status is next, or when a pending ticket human check reaches Done. Mark checked
resolves it through the native completion API. Ask someone else moves it to the
new owner. Resolved and reassigned rows stay in place until the next visit, with
revision-bound Undo. Reassignment Undo is available only to its original actor
for that exact latest unchanged assignment, and still requires write access
and a previous owner with current access. A subsequent edit invalidates it.

Requests are a live projection, not another persisted queue. The source ticket
creation/status/edit and definition events audit why a request became relevant;
`node.person_step_reassigned`, `entry_check.ticked` and the server-written human-check
completion audit routing and resolution. Repeating the current state makes no
new write or event. Every mutation binds the ticket revision, and manual-step
mutations also bind the definition revision. The append-only events retain old
owners and completions even when a definition changes.

The ticket list reserves a quiet person-check icon slot. Its tooltip lists all
active steps and owners. The Waiting for a person filter includes Waiting for
you and Not waiting. The additive `person_waiting` API filter/facet uses
`pending`, `mine` and `none`; the older `human_check` API filter keeps its
published pending-text semantics. An inactive human check remains visible on
the ticket but creates no Desk request before Done.

The native endpoints are:

- `GET /nodes/{nodeId}/person-steps`
- `GET /nodes/{nodeId}/person-step-owners?check_id=…&q=…&after=…` (50 people per page)
- `PUT /nodes/{nodeId}/person-steps/{checkId}`
- `PUT /nodes/{nodeId}/person-steps/{checkId}/owner`

The authored contract is in `api/areas/entry-checks.yaml`. Migration 1353 adds
only invoker functions and an event lookup index. It adds no tables or columns;
no new DSAR storage classification or permissions are needed. The Desk refuses
coverage above 1000 candidate tickets with 422 rather than presenting partial
totals. Existing tenant
and project RLS still apply.

In Project settings › Definitions, a free-text item starts with the ticket’s
person assignee or a project admin, or a named person chosen while adding it.
Its row menu’s “Who ticks this…” form chooses between the ticket’s assignee
with a named fallback (or a project admin), and one named person for every
ticket. Both choices stay visible while editing. The row describes who ticks
the item and any difference from the workspace default; changing only the
fallback counts as a definition difference. Saves retain the definition
revision checks and the owner’s current project-access validation.
