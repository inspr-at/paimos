// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"sort"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type parentPlacement struct {
	ParentID              string  `json:"parent_node_id"`
	ReleaseID             *string `json:"release_node_id"`
	Exists                bool    `json:"exists,omitempty"`
	RetainedScopeRequired *bool   `json:"retained_scope_revision_required,omitempty"`
}

// Match the paired tree writers: pairing -> tree -> tenant access fence ->
// resource rows -> event counter. Recheck authorization under that fence.
func lockMembership(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	for _, key := range []string{"aeon-pairing:" + p.TenantID, p.TenantID} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return err
		}
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID).Scan(&id); err != nil {
		return err
	}
	if authz.RequireTx(ctx, tx, p, "releases.write", authz.Scope{ProjectID: project}) != nil {
		return fail(403, "project access required")
	}
	return nil
}

// Bound expansion before loading memberships or mutating anything. A parent
// plus a selected descendant is one leaf set, with deterministic row order.
func expandWorkLeaves(ctx context.Context, tx pgx.Tx, project string, ids []string) ([]string, []string, map[string]bool, error) {
	leaves := map[string]bool{}
	out := []string{}
	parents := []string{}
	fromParents := map[string]bool{}
	for _, id := range ids {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id
   WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug IN ('work','ticket'))`, id, project).Scan(&valid); err != nil {
			return nil, nil, nil, err
		}
		if !valid {
			return nil, nil, nil, fail(404, "work node not found in project")
		}
		var parent bool
		if err := tx.QueryRow(ctx, `SELECT NOT aeon_work_is_release_leaf(current_setting('aeon.tenant_id')::uuid,$1::uuid)`, id).Scan(&parent); err != nil {
			return nil, nil, nil, err
		}
		// The scope helper independently bounds traversal to 50,000 nodes.
		// This API's tighter limit counts leaves, not intermediate parents.
		rows, err := tx.Query(ctx, `SELECT id::text FROM aeon_work_release_scope(ARRAY[$1::uuid]) WHERE project_id=$2 AND kind_slug IN ('work','epic','ticket','task') AND is_leaf ORDER BY id LIMIT 1001`, id, project)
		if err != nil {
			return nil, nil, nil, err
		}
		rootLeaves := []string{}
		for rows.Next() {
			var node string
			if err = rows.Scan(&node); err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
			rootLeaves = append(rootLeaves, node)
			if !leaves[node] {
				leaves[node] = true
				out = append(out, node)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, nil, err
		}
		if len(rootLeaves) > 1000 || len(leaves) > 1000 {
			return nil, nil, nil, fail(409, "parent release placement exceeds 1000 leaves")
		}
		if parent {
			parents = append(parents, id)
			for _, leaf := range rootLeaves {
				fromParents[leaf] = true
			}
		}
	}
	sort.Strings(parents)
	return out, parents, fromParents, nil
}

func placeParents(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, release string, parents []string, before, after *membershipSnapshot) error {
	for _, id := range parents {
		old := parentPlacement{ParentID: id}
		err := tx.QueryRow(ctx, `SELECT release_node_id::text,(retained_membership->>'scope_revision_required')::boolean
   FROM work_parent_releases WHERE parent_node_id=$1 AND project_node_id=$2`, id, project).Scan(&old.ReleaseID, &old.RetainedScopeRequired)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		old.Exists = err == nil
		placed := parentPlacement{ParentID: id, ReleaseID: &release, Exists: true}
		// Scope approval belongs to the prior placement. Invalidate it even if
		// a later ordinary placement returns to that release; only Undo may
		// restore the exact previous flag recorded in the event snapshot.
		if err = tx.QueryRow(ctx, `INSERT INTO work_parent_releases(tenant_id,parent_node_id,project_node_id,release_node_id) VALUES($1,$2,$3,$4)
   ON CONFLICT(tenant_id,parent_node_id) DO UPDATE SET project_node_id=excluded.project_node_id,release_node_id=excluded.release_node_id,
    retained_membership=CASE WHEN work_parent_releases.retained_membership IS NOT NULL AND
     ROW(work_parent_releases.project_node_id,work_parent_releases.release_node_id) IS DISTINCT FROM ROW(excluded.project_node_id,excluded.release_node_id)
     THEN jsonb_set(work_parent_releases.retained_membership,'{scope_revision_required}','true'::jsonb)
     ELSE work_parent_releases.retained_membership END
   RETURNING (retained_membership->>'scope_revision_required')::boolean`, p.TenantID, id, project, release).Scan(&placed.RetainedScopeRequired); err != nil {
			return err
		}
		before.Parents = append(before.Parents, old)
		after.Parents = append(after.Parents, placed)
	}
	return nil
}
