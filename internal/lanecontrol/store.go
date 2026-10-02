// SPDX-License-Identifier: AGPL-3.0-only
package lanecontrol

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type lane struct {
	id, project, owner string
	revision           int64
	policy             policy
	limit              int64
	start, end         time.Time
}

func currentLane(ctx context.Context, tx pgx.Tx, tenantID, id string, revision int64, now time.Time) (lane, error) {
	var l lane
	var enabled, paused bool
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT l.node_id::text,l.project_id::text,l.owner_principal_id::text,l.revision,l.enabled,l.paused,l.policy
 FROM autopilot_lanes l JOIN nodes n ON n.tenant_id=l.tenant_id AND n.id=l.node_id
 JOIN principals p ON p.tenant_id=l.tenant_id AND p.id=l.owner_principal_id
 WHERE l.tenant_id=$1 AND l.node_id=$2 AND n.deleted_at IS NULL AND p.kind='person' AND p.status='active'
 FOR NO KEY UPDATE OF l`, tenantID, id).Scan(&l.id, &l.project, &l.owner, &l.revision, &enabled, &paused, &raw)
	if err != nil {
		return l, err
	}
	if !enabled || paused || l.revision != revision {
		return l, workorders.Fail(409, "lane disabled, paused or revision changed")
	}
	owner := tenant.Principal{ID: l.owner, TenantID: tenantID, Kind: tenant.Person}
	for _, perm := range []string{"nodes.read", "run.create", "work_orders.write", "work_orders.assign", "runs.write"} {
		if err = authz.RequireTx(ctx, tx, owner, perm, authz.Scope{ProjectID: l.project}); err != nil {
			return l, err
		}
	}
	l.policy, l.limit, err = parsePolicy(raw)
	if err != nil {
		return l, err
	}
	l.start, l.end, err = activeWindow(l.policy.Window, now)
	if err != nil {
		return l, workorders.Fail(409, err.Error())
	}
	return l, nil
}

type Reservation struct {
	// DispatchID is the scheduler's durable idempotency identity, not a new queue.
	DispatchID, LaneID, TicketID, RunID string
	LaneRevision                        int64
	MaximumMS, AttemptMS                int64
}

// ReserveTx binds the scheduler's first queued run to one build/review/fix
// envelope. It must be called in the dispatch transaction, under the tenant /
// tree fence, before event append. An error must roll back that transaction.
// No public endpoint exposes this internal authority to workers.
func ReserveTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Reservation, now time.Time) error {
	if !workorders.UUID(in.DispatchID) || !workorders.UUID(in.LaneID) || !workorders.UUID(in.TicketID) || !workorders.UUID(in.RunID) || in.MaximumMS <= StopAllowanceMS || in.MaximumMS > maxBudgetMS || in.AttemptMS <= StopAllowanceMS || in.AttemptMS > in.MaximumMS {
		return workorders.Fail(400, "invalid lane envelope")
	}
	l, err := currentLane(ctx, tx, p.TenantID, in.LaneID, in.LaneRevision, now)
	if err != nil {
		return err
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT lane_id=$2 AND ticket_node_id=$3 AND lane_revision=$4 AND maximum_ms=$5 AND attempt_ms=$6 AND NOT closed FROM lane_budget_envelopes WHERE id=$1`, in.DispatchID, in.LaneID, in.TicketID, in.LaneRevision, in.MaximumMS, in.AttemptMS).Scan(&same)
	if err == nil {
		if !same {
			return workorders.Fail(409, "dispatch envelope conflict")
		}
		return BindRunTx(ctx, tx, in.DispatchID, in.RunID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Only an unclaimed queued run under this ticket can consume the reservation.
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id JOIN nodes t ON t.tenant_id=n.tenant_id AND t.id=n.parent_id WHERE r.id=$1 AND r.status='queued' AND r.lane_envelope_id IS NULL AND n.parent_id=$2 AND t.project_id=$3 AND n.deleted_at IS NULL AND t.deleted_at IS NULL)`, in.RunID, in.TicketID, l.project).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return workorders.Fail(409, "lane requires a queued run of its project ticket")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO lane_budget_periods(tenant_id,lane_id,project_id,starts_at,ends_at,limit_ms) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, p.TenantID, l.id, l.project, l.start, l.end, l.limit); err != nil {
		return err
	}
	// A changed limit may narrow future admission but cannot erase old holds or
	// raise this already-approved period ceiling. Revised windows cannot overlap.
	var overlap bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lane_budget_periods WHERE lane_id=$1 AND starts_at<>$2 AND starts_at<$3 AND ends_at>$2)`, l.id, l.start, l.end).Scan(&overlap); err != nil {
		return err
	}
	if overlap {
		return workorders.Fail(409, "lane window changed during a budget period")
	}
	tag, err := tx.Exec(ctx, `UPDATE lane_budget_periods SET held_ms=held_ms+$3 WHERE lane_id=$1 AND starts_at=$2 AND settled_ms+held_ms+$3<=LEAST(limit_ms,$4)`, l.id, l.start, in.MaximumMS, l.limit)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return workorders.Fail(409, "lane period budget exhausted")
	}
	_, err = tx.Exec(ctx, `INSERT INTO lane_budget_envelopes(tenant_id,id,project_id,lane_id,period_start,ticket_node_id,owner_principal_id,lane_revision,maximum_ms,attempt_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.TenantID, in.DispatchID, l.project, l.id, l.start, in.TicketID, l.owner, in.LaneRevision, in.MaximumMS, in.AttemptMS)
	if err != nil {
		return err
	}
	return BindRunTx(ctx, tx, in.DispatchID, in.RunID)
}

// BindRunTx is the explicit scheduler/lifecycle handoff for subsequent attempts.
// It does not mint a budget or authorize a start. All retries still pass ClaimTx.
func BindRunTx(ctx context.Context, tx pgx.Tx, envelopeID, runID string) error {
	tag, err := tx.Exec(ctx, `UPDATE agent_runs r SET lane_envelope_id=e.id FROM lane_budget_envelopes e,nodes n
 WHERE e.id=$1 AND NOT e.closed AND r.id=$2 AND r.status='queued'
 AND (r.lane_envelope_id IS NULL OR r.lane_envelope_id=e.id)
 AND n.tenant_id=r.tenant_id AND n.id=r.work_order_id AND n.parent_id=e.ticket_node_id AND n.project_id=e.project_id AND n.deleted_at IS NULL`, envelopeID, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return workorders.Fail(409, "run cannot bind to lane envelope")
	}
	return nil
}

// GuardRunTx catches older callers that created a retry/review without carrying
// the dispatch binding. No claim path can silently treat lane work as manual.
func GuardRunTx(ctx context.Context, tx pgx.Tx, runID string) error {
	var unbound bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
 SELECT n.id,n.parent_id,1 AS depth FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id WHERE r.id=$1
 UNION ALL SELECT n.id,n.parent_id,a.depth+1 FROM ancestors a JOIN nodes n ON n.id=a.parent_id WHERE a.depth<64
 ) SELECT EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=$1 AND r.lane_envelope_id IS NULL AND (
 EXISTS(SELECT 1 FROM lane_budget_envelopes e JOIN ancestors a ON a.id=e.ticket_node_id WHERE NOT e.closed)
 OR EXISTS(SELECT 1 FROM agent_runs earlier WHERE earlier.work_order_id=r.work_order_id AND earlier.lane_envelope_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM ancestors WHERE depth=64 AND parent_id IS NOT NULL)))`, runID).Scan(&unbound)
	if err != nil {
		return err
	}
	if unbound {
		return workorders.Fail(409, "lane child or retry requires an explicit shared envelope binding")
	}
	return nil
}

// CloseTx releases only provably unused envelope budget. An uncertain process
// keeps its grant and blocks closure, including after cancellation or restart.
func CloseTx(ctx context.Context, tx pgx.Tx, id string) error {
	var laneID string
	var start time.Time
	var remaining int64
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT lane_id::text,period_start,maximum_ms-settled_ms,closed FROM lane_budget_envelopes WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&laneID, &start, &remaining, &closed); err != nil {
		return err
	}
	if closed {
		return nil
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lane_attempt_grants WHERE envelope_id=$1 AND elapsed_ms IS NULL)`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return workorders.Fail(409, "lane process settlement unknown")
	}
	if _, err := tx.Exec(ctx, `UPDATE lane_budget_periods SET held_ms=held_ms-$3 WHERE lane_id=$1 AND starts_at=$2`, laneID, start, remaining); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE lane_budget_envelopes SET closed=true WHERE id=$1`, id)
	return err
}
