// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func writeWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Request) (string, int64, []nodes.Event, error) {
	var mutation nodes.RoutineMutation
	var err error
	if in.Kind == "work.create" {
		v := in.WorkCreate
		mutation, err = nodes.CreateRoutineWorkTx(ctx, tx, p, in.TargetID, v.Title, v.Body)
	} else {
		v := in.WorkUpdate
		mutation, err = nodes.UpdateRoutineWorkTx(ctx, tx, p, in.TargetID, in.ProjectID, in.ExpectedRevision, v.Title, v.Body, v.State)
	}
	return mutation.ID, mutation.Revision, mutation.Events, err
}
