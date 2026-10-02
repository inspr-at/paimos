// SPDX-License-Identifier: AGPL-3.0-only

// Package lanedispatch shares exclusive ticket workflow ownership between the
// lane scheduler and coordinator. Requests grant no daemon execution authority.
package lanedispatch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Request struct {
	ID                     string    `json:"id"`
	ProjectID              string    `json:"project_id"`
	LaneID                 *string   `json:"lane_id"`
	TicketNodeID           string    `json:"ticket_node_id"`
	TicketRevision         time.Time `json:"ticket_revision"`
	LaneRevision           int64     `json:"lane_revision"`
	RunID                  *string   `json:"run_id"`
	Phase                  string    `json:"phase"`
	Revision               int64     `json:"revision"`
	DraftCriteria          []string  `json:"draft_criteria"`
	DraftEstimateHours     float64   `json:"draft_estimate_hours"`
	RequestedByPrincipalID string    `json:"requested_by_principal_id"`
	CreatedAt              time.Time `json:"created_at"`
	Launched               bool      `json:"launched"`
}

const Columns = `id::text,project_id::text,lane_id::text,ticket_node_id::text,ticket_revision,lane_revision,run_id::text,phase,revision,draft_criteria,draft_estimate_hours::float8,requested_by_principal_id::text,created_at`

func Scan(row pgx.Row) (Request, error) {
	var r Request
	var draft []byte
	err := row.Scan(&r.ID, &r.ProjectID, &r.LaneID, &r.TicketNodeID, &r.TicketRevision, &r.LaneRevision, &r.RunID, &r.Phase, &r.Revision, &draft, &r.DraftEstimateHours, &r.RequestedByPrincipalID, &r.CreatedAt)
	if err == nil {
		err = json.Unmarshal(draft, &r.DraftCriteria)
	}
	return r, err
}
func Load(ctx context.Context, tx pgx.Tx, id string, lock bool) (Request, error) {
	q := `SELECT ` + Columns + ` FROM lane_dispatches WHERE id=$1`
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	return Scan(tx.QueryRow(ctx, q, id))
}

// LockTenant is first, before pairing, tree, ticket/order/run/dispatch rows,
// accounts and finally the event counter. The caller acquires the remaining
// locks it needs and authorizes inside this transaction before any mutation.
func LockTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, tenantID).Scan(&id)
}

// Owned reports lane ownership, including pending preparation/person gates.
// Coordinator requests remain on the existing queue/run path.
func Owned(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error) {
	var owned bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lane_dispatches WHERE ticket_node_id=$1 AND lane_id IS NOT NULL AND phase NOT IN ('completed','cancelled'))`, ticketID).Scan(&owned)
	return owned, err
}

// Claim requires the tenant/tree fence and a locked, live in-scope ticket.
// ON CONFLICT and the partial unique index are an additional ownership fence.
// Coordinator terminal requests may be retired; lane workflows need an explicit
// lifecycle decision, so a process exit never releases their ownership.
func Claim(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Request) (*Request, error) {
	if _, err := tx.Exec(ctx, `UPDATE lane_dispatches d SET phase='completed',revision=revision+1 WHERE d.ticket_node_id=$1 AND d.lane_id IS NULL AND d.phase='coordinator_requested' AND EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=d.run_id AND r.status IN ('completed','failed','cancelled'))`, in.TicketNodeID); err != nil {
		return nil, err
	}
	r, err := Scan(tx.QueryRow(ctx, `INSERT INTO lane_dispatches(tenant_id,project_id,lane_id,ticket_node_id,ticket_revision,lane_revision,run_id,phase,requested_by_principal_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(tenant_id,ticket_node_id) WHERE phase NOT IN ('completed','cancelled') DO NOTHING RETURNING `+Columns, p.TenantID, in.ProjectID, in.LaneID, in.TicketNodeID, in.TicketRevision, in.LaneRevision, in.RunID, in.Phase, p.ID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

// OwnedOrder fences ticket work orders at any ancestor depth, including nested
// review/fix orders. A chain beyond the explicit 64-node bound fails closed.
func OwnedOrder(ctx context.Context, tx pgx.Tx, orderID string) (bool, error) {
	var owned bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
 SELECT id,parent_id,0 AS depth FROM nodes WHERE id=$1
 UNION ALL SELECT n.id,n.parent_id,a.depth+1 FROM ancestors a JOIN nodes n ON n.id=a.parent_id WHERE a.depth<64
 ) SELECT EXISTS(SELECT 1 FROM lane_dispatches d JOIN ancestors a ON a.id=d.ticket_node_id WHERE d.lane_id IS NOT NULL AND d.phase NOT IN ('completed','cancelled'))
 OR EXISTS(SELECT 1 FROM ancestors WHERE depth=64 AND parent_id IS NOT NULL)`, orderID).Scan(&owned)
	return owned, err
}
