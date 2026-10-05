// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A missing/denied release is a missing scope, never a successful all-work read.
func validateReleaseScope(ctx context.Context, tx pgx.Tx, q listQuery) error {
	if len(q.ShipsIn)+len(q.ShipsInNot) == 0 {
		return nil
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok {
		return authz.ErrForbidden
	}
	for _, id := range append(append([]string{}, q.ShipsIn...), q.ShipsInNot...) {
		if id == "none" {
			continue
		}
		var project string
		err := tx.QueryRow(ctx, `SELECT r.project_node_id::text FROM project_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL JOIN project_delivery d ON d.tenant_id=r.tenant_id AND d.project_node_id=r.project_node_id JOIN nodes pn ON pn.tenant_id=d.tenant_id AND pn.id=d.project_node_id AND pn.deleted_at IS NULL WHERE r.tenant_id=$1 AND r.release_node_id=$2 AND ($3::uuid IS NULL OR r.project_node_id=$3)`, p.TenantID, id, q.Within).Scan(&project)
		if err == pgx.ErrNoRows {
			return notFound("release scope not found")
		}
		if err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
	}
	return nil
}
