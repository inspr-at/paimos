// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// EnqueueSystem prepares untargeted queue work for a newly created ticket. The
// caller has authorized the saved automation, holds tenant/tree/node locks, and
// must append these changes after all remaining writes. No event counter is
// taken here. The same readiness, typed orders and rank helpers serve manual
// queue entry; inert runs cannot be picked up before coordinator routing.
func EnqueueSystem(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID string) ([]events.Change, error) {
	var title, body, kind, state string
	var fields, node json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.title,n.body,k.slug,n.state,n.fields,to_jsonb(n)-'tenant_id' FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1`, nodeID).Scan(&title, &body, &kind, &state, &fields, &node)
	if err != nil {
		return nil, err
	}
	ready := Check(kind, state, title, body, Fields(fields), false)
	if !ready.Ready {
		return nil, fmt.Errorf("recurrence ticket is not ready for the queue")
	}
	// This is the established tenant-keyed holder identity from queueAdd.
	var inert bool
	if err = tx.QueryRow(ctx, `SELECT kind='agent' AND name='Next free agent (queue holder)' AND NOT EXISTS(SELECT 1 FROM agent_keys WHERE principal_id=$1) FROM principals WHERE id=$1`, p.TenantID).Scan(&inert); err != nil {
		return nil, err
	}
	if !inert {
		return nil, fmt.Errorf("queue holder identity conflicts")
	}
	order, changes, err := workorders.CreateDeferred(ctx, tx, p, workorders.CreateInput{Title: title, Body: body, Parent: &nodeID, Criteria: Criteria(Fields(fields))})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE work_orders SET status='ready',revision=revision+1 WHERE node_id=$1`, order.NodeID); err != nil {
		return nil, err
	}
	rank, err := AppendRank(ctx, tx)
	if err != nil {
		return nil, err
	}
	var run json.RawMessage
	err = tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_rank,queue_security_review_required)
 VALUES($1,$2,$1,$3,$4,clock_timestamp(),$5,$6) RETURNING to_jsonb(agent_runs)-'tenant_id'`, p.TenantID, order.NodeID, nodeID, p.ID, rank, ready.SecurityReviewRequired).Scan(&run)
	if err != nil {
		return nil, err
	}
	before := node
	if ready.SecurityReviewRequired {
		err = tx.QueryRow(ctx, `UPDATE nodes SET fields=fields||'{"security_review_required":true,"needs_review":true,"review_route":"review-gate"}'::jsonb,updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes)-'tenant_id'`, nodeID).Scan(&node)
		if err != nil {
			return nil, err
		}
	}
	changes = append(changes, events.Change{NodeID: &nodeID, Type: "queue.added", Before: before, After: node}, events.Change{NodeID: &order.NodeID, Type: "run.created", After: run})
	return changes, nil
}
