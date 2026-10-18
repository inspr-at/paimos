// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"fmt"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Append last, after every registration/binding write and lock. The node is the
// ticket itself so its history shows who authorized parallel work and why.
func recordParallelBuild(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) error {
	if s.ParallelReason == nil {
		return nil
	}
	ticket, err := workqueue.BuildTicketTx(ctx, tx, s.TicketNodeID, s.WorkOrderID, s.RunID)
	if err != nil || ticket == nil {
		return err
	}
	if err := workorders.Record(ctx, tx, p, *ticket, "harness.parallel_build_started", nil, map[string]any{
		"session_id": s.ID, "parallel_reason": *s.ParallelReason, "started_by_principal_id": p.ID,
	}); err != nil {
		return err
	}
	return workorders.Record(ctx, tx, p, *ticket, "comment.created", nil, map[string]any{
		"body_markdown": fmt.Sprintf("Parallel work authorized for session %s: %s", s.ID, *s.ParallelReason),
	})
}

func recordBuildContinuation(ctx context.Context, tx pgx.Tx, p tenant.Principal, admission workorders.BuildAdmission, session string) error {
	if batch, ok := ctx.Value(controlEventKey{}).(*controlEventBatch); ok {
		for _, change := range admission.Changes(session, "") {
			if err := batch.addEvent(p, *change.NodeID, change.Type, change.Before, change.After); err != nil {
				return err
			}
		}
		return nil
	}
	return admission.Record(ctx, tx, p, session, "")
}
