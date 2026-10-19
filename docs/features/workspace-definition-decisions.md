<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Workspace definitions and project decisions

Settings → Policies contains the workspace definition, after Cross-family review.
Projects using the default read its latest checks immediately. A project's own
definition is a complete copy, including when it has no checks.

A later default change waits as one decision per added, changed or removed check.
Until a project administrator accepts it, the project's enforced checks stay as
saved. Keeping a check acknowledges that version only; another default edit can
wait again. The original copy snapshot and `created_at` copy date remain intact.
Keeping a version also retains the enforced policy revision, so existing person
ticks and step owner assignments remain valid.

Project administrators see a quiet Decision Desk item linking to the notice in
Project settings → Definitions. Viewers can read the notice and waiting checks.
They cannot decide. The item disappears once every change has been decided.
Actions recheck current project administration and both displayed revisions under
the tenant/tree fence. Each decision appends `definition.change_decided`.

During a visit, deciding keeps each row and the notice in place. Removed rows
retain Undo. The last decision changes the notice to “All changes decided.”;
the next visit omits it. The workspace's differing-project list counts actual
copy differences and uses keyset pages. Coverage over 1000 projects is reported
as an error instead of partial counts.

The additive API is authored in `api/areas/entry-checks.yaml` and
`api/areas/decision-desk.yaml`. Decisions and snapshots use existing tenant-scoped
definition nodes and events; no migration or new permission is introduced.
