// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/lanecontrol"
	"github.com/jackc/pgx/v5"
)

// Exercise the real claim and telemetry handlers, including rollback, wire
// bindings, historical replay and charging exactly once across both endpoints.
func TestLaneClaimAndSettlementHTTP(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	ticket := f.ticket(t, "open", "medium", nil)
	var project, lane string
	now := time.Now().UTC()
	policy, err := json.Marshal(map[string]any{
		"parallel_limit": 1, "budget": map[string]any{"agent_hours": 1},
		"window": map[string]any{"timezone": "UTC", "days": []int{1, 2, 3, 4, 5, 6, 7}, "start": now.Add(-6 * time.Hour).Format("15:04"), "end": now.Add(6 * time.Hour).Format("15:04")},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		ctx := t.Context()
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Lane project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, ticket, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, o.NodeID, ticket); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'autopilot_lane','Lane','LANE','agents',ARRAY[]::text[])`, f.person.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Lane',$2 FROM node_kinds WHERE slug='autopilot_lane' RETURNING id::text`, f.person.TenantID, project).Scan(&lane); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO autopilot_lanes(tenant_id,node_id,project_id,owner_principal_id,name,enabled,policy) VALUES($1,$2,$3,$4,'Lane',true,$5)`, f.person.TenantID, lane, project, f.person.ID, policy); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working','{"cap":1}')`, f.person.TenantID, f.person.ID)
		return err
	})
	run := f.run(t, o)
	ids := f.reserve(t, run)
	roundTheClock(t, f, run.ID)
	envelope := uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		ctx := t.Context()
		if err := authz.LockProjectMutation(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET billing_mode='subscription' WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET unit='percent',allowance=100,used=0,reserved=1,capacity_kind='5h',capacity_bucket='primary',capacity_source='harness',capacity_read_at=clock_timestamp(),capacity_allowed=true WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_reservations SET reserved_units=1 WHERE run_id=$1`, run.ID); err != nil {
			return err
		}
		return lanecontrol.ReserveTx(ctx, tx, f.person, lanecontrol.Reservation{DispatchID: envelope, LaneID: lane, TicketID: ticket, RunID: run.ID, LaneRevision: 1, MaximumMS: 60000, AttemptMS: 40000}, now)
	})
	path := "/api/runs/" + run.ID
	legacy, _ := json.Marshal(claimBody(ids))
	w := f.request(f.agent, "POST", path+"/claim", string(legacy), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "lane-capable") {
		t.Fatalf("legacy claim: %d %s", w.Code, w.Body.String())
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM lane_attempt_grants`); n != 0 {
		t.Fatal("refusal created a grant")
	}
	body := claimBody(ids)
	body["lane_capability"] = lanecontrol.Capability
	body["lane_workspace_id"] = run.ID
	f.call(t, f.agent, "POST", path+"/claim", body, 200, &run)
	g := run.LaneGrant
	if run.Status != "starting" || g == nil || g.EnvelopeID != envelope || g.ProjectID != project || g.WorkspaceID != run.ID || g.RemainingMS != 40000 {
		t.Fatalf("claim binding: %+v", run)
	}
	var replay agentruns.Run
	f.call(t, f.agent, "POST", path+"/claim", body, 200, &replay)
	if replay.LaneGrant == nil || !replay.LaneGrant.ExpiresAt.Equal(g.ExpiresAt) || replay.LaneGrant.RemainingMS > g.RemainingMS {
		t.Fatalf("claim replay replenished grant: %+v", replay.LaneGrant)
	}
	body["lane_workspace_id"] = uuid()
	f.call(t, f.agent, "POST", path+"/claim", body, 409, nil)
	settlement := map[string]any{"elapsed_ms": 15000, "exit_confirmed": true}
	report := map[string]any{"sequence": 1, "kind": "finished", "status": "ownership_lost", "lane_settlement": settlement}
	f.call(t, f.agent, "POST", path+"/telemetry", report, 400, nil)
	report["status"] = "completed"
	f.call(t, f.agent, "POST", path+"/telemetry", report, 200, &run)
	f.call(t, f.agent, "POST", path+"/telemetry", report, 200, nil)
	settlement["elapsed_ms"] = 16000
	f.call(t, f.agent, "POST", path+"/telemetry", report, 409, nil)
	if run.Status != "completed" {
		t.Fatalf("settlement status: %s", run.Status)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var held, settled, elapsed int64
		if err := tx.QueryRow(t.Context(), `SELECT p.held_ms,p.settled_ms,g.elapsed_ms FROM lane_budget_periods p JOIN lane_attempt_grants g ON g.project_id=p.project_id WHERE g.run_id=$1`, run.ID).Scan(&held, &settled, &elapsed); err != nil {
			return err
		}
		if held != 45000 || settled != 15000 || elapsed != 15000 {
			t.Fatalf("accounting: held=%d settled=%d elapsed=%d", held, settled, elapsed)
		}
		return nil
	})
}
