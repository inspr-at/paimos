// SPDX-License-Identifier: AGPL-3.0-only
package lanecontrol

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// ClaimTx is called only after exact account reservation, authenticated daemon
// ownership, generation and ordinary account admission have been checked. The
// tenant/tree/pairing fence is held before all row locks, through final commit.
func ClaimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID, capability, workspaceID, daemon, generation string, now time.Time) (*Grant, error) {
	if err := GuardRunTx(ctx, tx, runID); err != nil {
		return nil, err
	}
	var eid *string
	var accountID *string
	var state, area, harness string
	err := tx.QueryRow(ctx, `SELECT r.lane_envelope_id::text,r.account_id::text,r.status,coalesce(t.fields->>'area',''),coalesce(m.harness,'') FROM agent_runs r JOIN nodes n ON n.id=r.work_order_id LEFT JOIN nodes t ON t.id=n.parent_id LEFT JOIN model_profiles m ON m.id=r.model_profile_id WHERE r.id=$1`, runID).Scan(&eid, &accountID, &state, &area, &harness)
	if err != nil || eid == nil {
		return nil, err
	}
	if capability != Capability || !workorders.UUID(workspaceID) {
		return nil, workorders.Fail(409, "lane-capable daemon and isolated workspace required")
	}
	// An exact claim replay retains the first deadline, including after the lane
	// window closes. It never creates another attempt or replenishes its grant.
	prior, err := loadGrant(ctx, tx, runID, now)
	if err == nil {
		if prior.DaemonID != daemon || prior.Generation != generation || prior.WorkspaceID != workspaceID {
			return nil, workorders.Fail(409, "lane claim binding conflict")
		}
		return &prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if state != "queued" || accountID == nil {
		return nil, workorders.Fail(409, "lane run is not awaiting claim")
	}
	var laneID, project, owner string
	var periodStart time.Time
	var revision, remaining, attempt int64
	var closed bool
	err = tx.QueryRow(ctx, `SELECT lane_id::text,project_id::text,owner_principal_id::text,lane_revision,maximum_ms-settled_ms,attempt_ms,closed,period_start FROM lane_budget_envelopes WHERE id=$1 FOR NO KEY UPDATE`, *eid).Scan(&laneID, &project, &owner, &revision, &remaining, &attempt, &closed, &periodStart)
	if err != nil {
		return nil, err
	}
	if closed || remaining <= StopAllowanceMS {
		return nil, workorders.Fail(409, "lane envelope exhausted or closed")
	}
	l, err := currentLane(ctx, tx, p.TenantID, laneID, revision, now)
	if err != nil {
		return nil, err
	}
	if l.owner != owner || l.project != project {
		return nil, workorders.Fail(409, "lane ownership changed")
	}
	if !l.start.Equal(periodStart) {
		return nil, workorders.Fail(409, "lane envelope belongs to an earlier window")
	}
	var bound bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
 JOIN nodes t ON t.tenant_id=n.tenant_id AND t.id=n.parent_id
 JOIN lane_budget_envelopes e ON e.tenant_id=r.tenant_id AND e.id=r.lane_envelope_id
 WHERE r.id=$1 AND t.id=e.ticket_node_id AND n.project_id=e.project_id AND t.project_id=e.project_id
 AND n.deleted_at IS NULL AND t.deleted_at IS NULL)`, runID).Scan(&bound); err != nil {
		return nil, err
	}
	if !bound {
		return nil, workorders.Fail(409, "lane ticket or work-order scope changed")
	}
	if err = agentaccounts.ValidateLaneCapacity(ctx, tx, runID, *accountID, now); err != nil {
		return nil, err
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM lane_attempt_grants g JOIN lane_budget_envelopes e ON e.id=g.envelope_id WHERE e.lane_id=$1 AND g.elapsed_ms IS NULL`, laneID).Scan(&active); err != nil {
		return nil, err
	}
	if active >= l.policy.ParallelLimit {
		return nil, workorders.Fail(409, "lane parallel limit reached")
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT value FROM user_preferences WHERE principal_id=$1 AND key='agents.working'`, owner).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(409, "person working target required")
	} else if err != nil {
		return nil, err
	}
	target, err := ParseWorkingPreference(raw)
	if err != nil {
		return nil, workorders.Fail(409, "invalid person working target")
	}
	rows, err := tx.Query(ctx, `SELECT area,harness FROM aeon_lane_working_slots($1,$2)`, owner, runID)
	if err != nil {
		return nil, err
	}
	slots := []slot{}
	for rows.Next() {
		var s slot
		if err = rows.Scan(&s.area, &s.harness); err != nil {
			rows.Close()
			return nil, err
		}
		slots = append(slots, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if !fitsSlots(target, slots, slot{area, harness}) {
		return nil, workorders.Fail(409, "person working slots unavailable")
	}
	maximum := min(remaining, attempt)
	// A work order may narrow the dispatch grant, never expand it. Unknown cost
	// ceilings cannot be qualified as subscription-only execution.
	var orderSeconds *int64
	var cost *int64
	if err = tx.QueryRow(ctx, `SELECT w.max_duration_seconds,w.max_cost_micros FROM work_orders w JOIN agent_runs r ON r.work_order_id=w.node_id WHERE r.id=$1`, runID).Scan(&orderSeconds, &cost); err != nil {
		return nil, err
	}
	if cost != nil {
		return nil, workorders.Fail(409, "lane dollar budgets are not qualified")
	}
	if orderSeconds != nil {
		if *orderSeconds <= 0 {
			return nil, workorders.Fail(409, "invalid attempt duration")
		}
		if *orderSeconds < maximum/1000+1 {
			maximum = min(maximum, *orderSeconds*1000)
		}
	}
	if maximum <= StopAllowanceMS {
		return nil, workorders.Fail(409, "insufficient remaining stop allowance")
	}
	expires := now.Add(time.Duration(maximum) * time.Millisecond)
	_, err = tx.Exec(ctx, `INSERT INTO lane_attempt_grants(tenant_id,run_id,project_id,envelope_id,workspace_id,daemon_id,daemon_generation,maximum_ms,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, runID, project, *eid, workspaceID, daemon, generation, maximum, expires)
	if err != nil {
		return nil, err
	}
	return &Grant{*eid, runID, project, workspaceID, daemon, generation, maximum, StopAllowanceMS, expires}, nil
}
func loadGrant(ctx context.Context, tx pgx.Tx, runID string, now time.Time) (Grant, error) {
	var g Grant
	var settled *int64
	var maximum int64
	err := tx.QueryRow(ctx, `SELECT envelope_id::text,run_id::text,project_id::text,workspace_id::text,daemon_id,daemon_generation,expires_at,elapsed_ms,maximum_ms FROM lane_attempt_grants WHERE run_id=$1`, runID).Scan(&g.EnvelopeID, &g.RunID, &g.ProjectID, &g.WorkspaceID, &g.DaemonID, &g.Generation, &g.ExpiresAt, &settled, &maximum)
	g.StopAllowanceMS = StopAllowanceMS
	g.RemainingMS = min(maximum, max(0, g.ExpiresAt.Sub(now).Milliseconds()))
	if settled != nil {
		g.RemainingMS = 0
	}
	return g, err
}

// SettleTx only frees a slot on positive evidence of owned-process exit. Usage
// beyond the grant is a protocol failure: retain the whole hold and slot for
// reconciliation instead of claiming a successful cap or discarding the excess.
func SettleTx(ctx context.Context, tx pgx.Tx, runID string, s *Settlement) error {
	if s == nil || !s.ExitConfirmed {
		return nil
	}
	if s.ElapsedMS < 0 {
		return workorders.Fail(400, "invalid lane elapsed time")
	}
	var eid, laneID string
	var period time.Time
	var maximum int64
	var old *int64
	err := tx.QueryRow(ctx, `SELECT g.envelope_id::text,e.lane_id::text,e.period_start,g.maximum_ms,g.elapsed_ms FROM lane_attempt_grants g JOIN lane_budget_envelopes e ON e.id=g.envelope_id WHERE g.run_id=$1 FOR NO KEY UPDATE OF g,e`, runID).Scan(&eid, &laneID, &period, &maximum, &old)
	if errors.Is(err, pgx.ErrNoRows) {
		return workorders.Fail(409, "lane settlement has no grant")
	}
	if err != nil {
		return err
	}
	if old != nil {
		if *old != s.ElapsedMS {
			return workorders.Fail(409, "divergent lane settlement")
		}
		return nil
	}
	if s.ElapsedMS > maximum {
		return workorders.Fail(409, "lane elapsed usage exceeds grant; reconciliation required")
	}
	if _, err = tx.Exec(ctx, `UPDATE lane_budget_periods SET settled_ms=settled_ms+$3,held_ms=held_ms-$3 WHERE lane_id=$1 AND starts_at=$2`, laneID, period, s.ElapsedMS); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE lane_budget_envelopes SET settled_ms=settled_ms+$2 WHERE id=$1`, eid, s.ElapsedMS); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE lane_attempt_grants SET elapsed_ms=$2 WHERE run_id=$1`, runID, s.ElapsedMS)
	return err
}
