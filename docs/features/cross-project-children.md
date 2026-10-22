# Cross-project child sessions

A child can register under a live session working in another project on the
selected instance when both projects are accessible and both generations have
the same verified canonical person and tenant. The child retains its project,
leaf work item, work shape, agent identity and run binding.

Registration accepts the parent's UUID through `parent_harness_session_id`
(CLI: `--parent-session`). Cross-project links are stored in
`harness_session_lineage` with the verified canonical person. Migration 1421
adds this table and tenant RLS; migration 0500 and its same-project composite
foreign key remain unchanged. Existing writers and readers continue using
`harness_sessions.parent_id` for same-project relationships.

Responses expose cross-project attribution as the optional
`cross_project_parent_harness_session_id`. The legacy parent field stays null
for these links. Detail, heartbeat and history retain the child's own bindings.
The Agents tree uses verified lineage when both rows are already visible; a
child remains a root if its parent is absent. Filtering to the child's project
keeps the child once. No ancestor is fetched to provide context. Inaccessible
parent references are omitted, including from cross-project event snapshots.

Cross-project lineage grants no process, execution, parent-control or reparent
authority. The exact key's explicit person or a run's immutable starting person
must resolve independently of the parent; a key issuer or machine is no fallback.
Unknown or mismatched persons, foreign tenants, inaccessible projects, inactive
parents, self-parenting, cycles and excessive depth are refused transactionally.
Tenant/tree fences precede sorted project fences and session locks. Permissions
are checked inside that final transaction, so access revocation cannot leave a
partially registered child or lineage record.

Existing same-project drag/drop and person reparent permissions are retained.
Cross-project lineage cannot be moved through those operations. The optional
response addition is declared as `harness-session/3.4`; old response fields and
routes remain unchanged. The UI reuses the existing rows, labels and tree
controls; needs Opus design for the coordinator's subsequent visual assessment.
