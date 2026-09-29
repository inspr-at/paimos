// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/workorders"
	"testing"
)

func TestPairedCodexClaimWithoutAllowanceSetup(t *testing.T) {
	f := newFixture(t)
	p := f.propose("codex")
	approved := f.approve(p, "connect_only")
	f.call("POST", "/api/agent-accounts/"+approved.Enrollments[0].AccountID+"/capacity/approve", nil, true, "", 204)
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/capacity/approve", nil, true, "", 204)
	var windows int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1`, e.AccountID).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if windows != 0 {
		t.Fatal("approval created an allowance window")
	}
	// Keep this deterministic at any wall-clock time: these are work hours,
	// not Sprint, a Run-now override or an invented manual allowance.
	schedule := capacity.DefaultSchedule()
	for i := range schedule.Week {
		schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	f.call("PUT", "/api/agent-accounts/capacity/schedule", map[string]any{"scope": "account", "account_id": e.AccountID, "schedule": schedule}, true, "", 204)
	var order workorders.Order
	decodeResult(t, f.call("POST", "/api/work-orders", map[string]any{"title": "First run", "criteria": []string{"Claim without a form"}, "assignee_principal_id": *v.PrincipalID}, true, "", 201), &order)
	decodeResult(t, f.call("PATCH", "/api/work-orders/"+order.NodeID, map[string]any{"expected_revision": order.Revision, "status": "ready"}, true, "", 200), &order)
	var run agentruns.Run
	decodeResult(t, f.call("POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 201), &run)
	var queued []agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, key, 200), &queued)
	if len(queued) != 1 || queued[0].ID != run.ID || queued[0].Wait != nil {
		t.Fatalf("paired first run missing or blocked: %+v", queued)
	}
	route := routeWithUnits(t, f, v, e, key, run.ID, map[string]int{"requests": 1}, 200)
	decodeResult(t, f.call("POST", "/api/runs/"+run.ID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": reservationIDs(route)}, false, key, 200), &run)
	if run.Status != "starting" {
		t.Fatalf("run not claimed: %s", run.Status)
	}
}
