// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
)

func TestPairedCapacityReadingsThroughAuthMiddleware(t *testing.T) {
	f := newFixture(t) // Real pairing, runtime key, auth middleware and account handler.
	p := f.propose("codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 20, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "agentd"}
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/readings", map[string]any{"readings": []capacity.Reading{reading}}, false, key, 204)
	// The new advice route must pass the real pairing boundary and route map.
	f.call("GET", "/api/agent-accounts/capacity/next?harness=codex", nil, false, key, 200)
	// account.probe alone must pass both authorization layers.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['account.probe'] WHERE principal_id=$1`, *v.PrincipalID); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/api/agent-accounts/capacity/next?harness=codex", nil, false, key, 200)
	var got []capacity.Reading
	decodeResult(t, f.call("GET", "/api/agent-accounts/"+e.AccountID+"/readings", nil, false, key, 200), &got)
	if len(got) != 1 || !got[0].ReadAt.Equal(now) || got[0].UsedPercent != 20 {
		t.Fatal("paired daemon could not read its observation")
	}
	other := f.propose("codex")
	f.approve(other, "connect_only")
	otherView := f.redeem(other)
	f.call("GET", "/api/agent-accounts/"+otherView.Enrollments[0].AccountID+"/readings", nil, false, key, 403)
	f.call("GET", "/api/agent-accounts/"+uuid(t, f.db)+"/readings", nil, false, key, 403)
	f.call("GET", "/api/agent-accounts/not-a-uuid/readings", nil, false, key, 403)
	f.call("GET", "/api/agent-accounts/"+e.AccountID, nil, false, key, 403)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['account.read'] WHERE principal_id=$1`, *v.PrincipalID); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/api/agent-accounts/"+e.AccountID+"/readings", nil, false, key, 403)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['account.probe'] WHERE principal_id=$1`, *v.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET state='revoked' WHERE account_id=$1`, e.AccountID); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/api/agent-accounts/"+e.AccountID+"/readings", nil, false, key, 403)
}

func TestObservedCapacityPreservesSeparatePairingApproval(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	// This test isolates approval; routing must not depend on local test time.
	schedule := capacity.DefaultSchedule()
	for i := range schedule.Week {
		schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	f.call("PUT", "/api/agent-accounts/capacity/schedule", map[string]any{"scope": "account", "account_id": e.AccountID, "schedule": schedule}, true, "", 204)
	now := time.Now().UTC().Add(-time.Second)
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 20, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness"}
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/readings", map[string]any{"readings": []capacity.Reading{reading}}, false, key, 204)
	// Approval, rather than the wall clock's work-hours band, must decide
	// whether this account can route an ordinary run.
	schedule := capacity.DefaultSchedule()
	schedule.Override = "sprint"
	f.call("PUT", "/api/agent-accounts/capacity/schedule", map[string]any{"scope": "user", "schedule": schedule}, true, "", 204)
	var approved bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT ongoing_approved_at IS NOT NULL FROM agent_pairing_enrollments WHERE account_id=$1`, e.AccountID).Scan(&approved); err != nil {
		t.Fatal(err)
	}
	if approved {
		t.Fatal("reading granted ongoing approval")
	}
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/capacity/approve", nil, false, key, 403)
	var verification, managed agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/"+*e.VerificationRunID, nil, false, key, 200), &verification)
	decodeResult(t, f.call("POST", "/api/work-orders/"+verification.OrderID+"/runs", map[string]string{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 201), &managed)
	routeWithUnits(t, f, v, e, key, managed.ID, map[string]int{"requests": 1}, 409)
	// Verification still consumes its exact original one-request reservation.
	check := routeWithUnits(t, f, v, e, key, verification.ID, map[string]int{"requests": 1}, 200)
	if len(check.Reservations) != 1 || check.Reservations[0].Unit != "requests" {
		t.Fatal(check)
	}
	f.claim(v, e, key, reservationIDs(check), 200)
	f.telemetry(v, e, key, 200)
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/capacity/approve", nil, true, "", 204)
	// Round the clock: the managed route must not depend on the hour the test runs.
	always := capacity.DefaultSchedule("UTC")
	for i := range always.Week {
		always.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	always.Reserve = capacity.ReserveOff
	f.call("PUT", "/api/agent-accounts/capacity/schedule", map[string]any{"scope": "user", "schedule": always}, true, "", 204)
	route := routeWithUnits(t, f, v, e, key, managed.ID, map[string]int{"requests": 1}, 200)
	if len(route.Reservations) != 1 || route.Reservations[0].Unit != "percent" {
		t.Fatal(route)
	}
	var manual int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND NOT pairing_verification AND capacity_kind IS NULL`, e.AccountID).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual != 0 {
		t.Fatal("approval created manual windows")
	}
}
