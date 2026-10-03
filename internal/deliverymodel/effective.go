// SPDX-License-Identifier: AGPL-3.0-only

package deliverymodel

// Effective is a SQL relation fragment. Join it as a subquery with a caller's
// alias; its columns are tenant_id, project_node_id, item_node_id, kind, state,
// created_at, release_node_id, release_rank, release_state, rank, revision,
// expedite, due_on and source. Callers must bind tenant/project/item predicates
// and bound lists before executing, in a tenant transaction with visibility.
//
// It returns only live permitted items in live releases-mode projects. No
// ships_in row means the unranked backlog tail (rank NULL, revision 0).
// Tombstones and hidden backlog kinds keep their stored placement but are
// absent from this read.
// New creates therefore need no placement write or maintenance trigger.
const Effective = `(
 SELECT n.tenant_id, d.project_node_id, n.id AS item_node_id,
        k.slug AS kind, n.state, n.created_at,
        s.release_node_id, r.rank AS release_rank, r.state AS release_state,
        s.rank, COALESCE(s.revision, 0) AS revision,
        CASE WHEN n.state IN ('done','accepted','delivered','cancelled','canceled')
             THEN false ELSE COALESCE(s.expedite, false) END AS expedite,
        s.due_on, s.source
 FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 JOIN project_delivery d ON d.tenant_id=n.tenant_id AND d.project_node_id=n.project_id
 JOIN nodes p ON p.tenant_id=d.tenant_id AND p.id=d.project_node_id AND p.deleted_at IS NULL
 JOIN node_kinds pk ON pk.tenant_id=p.tenant_id AND pk.id=p.kind_id AND pk.slug='project'
 LEFT JOIN ships_in s ON s.tenant_id=n.tenant_id AND s.project_node_id=d.project_node_id AND s.item_node_id=n.id
 LEFT JOIN project_releases r ON r.tenant_id=s.tenant_id AND r.project_node_id=s.project_node_id AND r.release_node_id=s.release_node_id
 WHERE n.deleted_at IS NULL AND k.slug IN ('epic','ticket','task')
)`
