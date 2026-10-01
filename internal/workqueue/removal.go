// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func Terminal(state string) bool {
	switch State(state) {
	case "done", "cancelled", "archived", "delivered", "accepted":
		return true
	default:
		return false
	}
}

// RemoveQueued cancels only unstarted work as part of an already-authorized
// ticket edit or queue removal. The caller owns the transaction and holds
// pairing then tree locks. Reopening cannot resurrect the run or its holds.
func RemoveQueued(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID string) (bool, error) {
	var id, order string
	err := tx.QueryRow(ctx, `SELECT id::text,work_order_id::text FROM agent_runs WHERE queue_node_id=$1 AND status='queued'`, nodeID).Scan(&id, &order)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM work_orders WHERE node_id=$1 FOR UPDATE`, order); err != nil {
		return false, err
	}
	var before json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT to_jsonb(r)-'tenant_id' FROM agent_runs r WHERE id=$1 AND status='queued' FOR UPDATE`, id).Scan(&before); err != nil {
		return false, err
	}
	removed, err := agentpairing.CancelQueuedRun(ctx, tx, id)
	if err != nil || !removed {
		return removed, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET account_id=NULL,started_at=coalesce(started_at,ended_at) WHERE id=$1`, id); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE work_orders SET status='cancelled',revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, order); err != nil {
		return false, err
	}
	err = workorders.Record(ctx, tx, p, nodeID, "queue.removed", before, map[string]string{"run_id": id})
	return true, err
}
