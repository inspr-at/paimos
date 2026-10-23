// SPDX-License-Identifier: AGPL-3.0-only
package activity

import (
	"context"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RoutineCommentTx shares the ordinary append-only comment representation.
// Caller holds tenant/tree and target rows; append the returned event only
// after recording the idempotent routine result. This cannot edit old comments.
func RoutineCommentTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, node, project, body string) (events.Change, error) {
	if err := authz.RequireTx(ctx, tx, p, "comments.write", authz.Scope{ProjectID: project}); err != nil {
		return events.Change{}, err
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug='work')`, node, project).Scan(&valid); err != nil {
		return events.Change{}, err
	}
	if !valid {
		return events.Change{}, workorders.Fail(404, "routine comment target unavailable")
	}
	return events.Change{NodeID: &node, Type: "comment.created", After: commentSnapshot{Body: body}}, nil
}
