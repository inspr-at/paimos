// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
)

// Budget isolation fixtures use explicit hours: manual caps are not vendor
// measurements and cannot exempt a managed run from the person's schedule.
func fixtureWorkHours(t *testing.T, f *fixture, accountID string) {
	t.Helper()
	s := capacity.DefaultSchedule("UTC")
	s.Reserve = capacity.ReserveOff
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	f.call("PUT", "/api/agent-accounts/capacity/schedule", map[string]any{"scope": "account", "account_id": accountID, "schedule": s}, true, "", 204)
}

func routeWithUnits(t *testing.T, f *fixture, v agentpairing.View, e agentpairing.Enrollment, key, run string, units map[string]int, status int) agentaccounts.RouteResult {
	t.Helper()
	w := f.call("POST", "/api/agent-accounts/route", map[string]any{"run_id": run, "daemon_id": *v.DaemonID, "account_ids": []string{e.AccountID}, "estimated_units": units}, false, key, status)
	var out agentaccounts.RouteResult
	if status == 200 {
		decodeResult(t, w, &out)
	}
	return out
}
func reservationIDs(r agentaccounts.RouteResult) []string {
	ids := []string{}
	for _, x := range r.Reservations {
		ids = append(ids, x.ReservationID)
	}
	return ids
}
func assertWindowLedger(t *testing.T, f *fixture, id string, used, reserved int64) {
	t.Helper()
	var gotUsed, gotReserved int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT used,reserved FROM account_allowance_windows WHERE id=$1`, id).Scan(&gotUsed, &gotReserved); err != nil {
		t.Fatal(err)
	}
	if gotUsed != used || gotReserved != reserved {
		t.Fatalf("window %s: used/reserved=%d/%d want %d/%d", id, gotUsed, gotReserved, used, reserved)
	}
}

func TestPairingVerificationAndOngoingBudgetsAreIsolated(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	// Quota details require the separate person link, even for the approver.
	offer := offerLink(t, f, p, key, e.AccountID)
	review := reviewLink(t, f, offer)
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 200)
	fixtureWorkHours(t, f, e.AccountID)
	var verificationWindow string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT id::text FROM account_allowance_windows WHERE account_id=$1 AND pairing_verification`, e.AccountID).Scan(&verificationWindow); err != nil {
		t.Fatal(err)
	}
	// Real person APIs create ordinary limits while the verification window is
	// still active. No test clock edits or direct allowance grants are needed.
	windows := map[string]agentaccounts.Window{}
	for _, unit := range []string{"requests", "tokens"} {
		w := f.call("POST", "/api/agent-accounts/"+e.AccountID+"/windows", map[string]any{"starts_at": time.Now().Add(-time.Second), "ends_at": time.Now().Add(time.Hour), "unit": unit, "allowance": 100, "pace_model": "unrestricted"}, true, "", 201)
		if strings.Contains(w.Body.String(), "pairing_verification") {
			t.Fatal("internal routing flag exposed in API")
		}
		var window agentaccounts.Window
		decodeResult(t, w, &window)
		windows[unit] = window
	}
	// The exemption is only for the generated pairing window; ordinary limits
	// of the same unit retain the existing overlap guard.
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/windows", map[string]any{"starts_at": time.Now(), "ends_at": time.Now().Add(time.Hour), "unit": "requests", "allowance": 10, "pace_model": "unrestricted"}, true, "", 409)
	f.probe(v, e, key, 200)
	var verification, managed agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/"+*e.VerificationRunID, nil, false, key, 200), &verification)
	decodeResult(t, f.call("POST", "/api/work-orders/"+verification.OrderID+"/runs", map[string]string{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 201), &managed)
	// Ordinary caps remain additive: missing the active token estimate refuses.
	routeWithUnits(t, f, v, e, key, managed.ID, map[string]int{"requests": 2}, 409)
	// Verification must not need or touch any ongoing estimate/window.
	check := routeWithUnits(t, f, v, e, key, verification.ID, map[string]int{"requests": 1}, 200)
	if len(check.Reservations) != 1 || check.Reservations[0].WindowID != verificationWindow {
		t.Fatal("verification reserved another budget")
	}
	assertWindowLedger(t, f, verificationWindow, 0, 1)
	for _, w := range windows {
		assertWindowLedger(t, f, w.ID, 0, 0)
	}
	replay := routeWithUnits(t, f, v, e, key, verification.ID, map[string]int{"requests": 1}, 200)
	if !slices.Equal(reservationIDs(check), reservationIDs(replay)) {
		t.Fatal("verification reservation replay changed")
	}
	// Window separation does not bypass the per-account parallel cap.
	routeWithUnits(t, f, v, e, key, managed.ID, map[string]int{"requests": 2, "tokens": 3}, 409)
	f.claim(v, e, key, reservationIDs(check), 200)
	f.telemetry(v, e, key, 200)
	assertWindowLedger(t, f, verificationWindow, 1, 0)
	for _, w := range windows {
		assertWindowLedger(t, f, w.ID, 0, 0)
	}
	var unexpired bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT ends_at>clock_timestamp() FROM account_allowance_windows WHERE id=$1`, verificationWindow).Scan(&unexpired); err != nil {
		t.Fatal(err)
	}
	if !unexpired {
		t.Fatal("test must route ongoing before verification expiry")
	}
	// The launcher catalog and normal account health must use the same ordinary
	// budgets as managed routing, not the exhausted internal verification cap.
	// Ownership was confirmed through the real account-link flow above;
	// pairing approval alone grants ongoing use, not quota visibility.
	var catalog agentaccounts.Catalog
	decodeResult(t, f.call("GET", "/api/agent-accounts/catalog", nil, true, "", 200), &catalog)
	found := false
	for _, host := range catalog.Hosts {
		for _, harness := range host.Harnesses {
			for _, account := range harness.Accounts {
				if account.ID != e.AccountID {
					continue
				}
				found = true
				if !account.Available {
					t.Fatal("verification cap blocked the regular-work launcher catalog")
				}
				if len(account.Windows) != 2 {
					t.Fatalf("owner catalog lost ordinary windows: got %d, want 2", len(account.Windows))
				}
				for _, window := range account.Windows {
					if window.ID == verificationWindow {
						t.Fatal("internal verification window leaked into ongoing account budgets")
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("paired account missing from launcher catalog")
	}
	ongoing := routeWithUnits(t, f, v, e, key, managed.ID, map[string]int{"requests": 2, "tokens": 3}, 200)
	if len(ongoing.Reservations) != 2 {
		t.Fatal("ordinary additive windows were dropped")
	}
	for _, r := range ongoing.Reservations {
		if r.WindowID != windows[r.Unit].ID {
			t.Fatal("managed run reserved a verification or foreign window")
		}
	}
	assertWindowLedger(t, f, verificationWindow, 1, 0)
	assertWindowLedger(t, f, windows["requests"].ID, 0, 2)
	assertWindowLedger(t, f, windows["tokens"].ID, 0, 3)
	replay = routeWithUnits(t, f, v, e, key, managed.ID, map[string]int{"requests": 2, "tokens": 3}, 200)
	if !slices.Equal(reservationIDs(ongoing), reservationIDs(replay)) {
		t.Fatal("managed reservation replay changed")
	}
	next := e
	next.VerificationRunID = &managed.ID
	f.claim(v, next, key, reservationIDs(ongoing), 200)
	for i := 0; i < 2; i++ {
		req := f.request("POST", "/api/runs/"+managed.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed", "turn_count_delta": 1, "input_tokens_delta": 5}, false, key)
		req.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
		req.Header.Set(agentruns.GenerationHeader, "test-generation")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("managed settlement/replay status %d: %s", w.Code, w.Body.String())
		}
	}
	assertWindowLedger(t, f, verificationWindow, 1, 0)
	assertWindowLedger(t, f, windows["requests"].ID, 1, 0)
	assertWindowLedger(t, f, windows["tokens"].ID, 5, 0)
}

func TestPairingVerificationCannotSubstituteAnotherPairingWindow(t *testing.T) {
	f := newFixture(t)
	p, v := f.twoQualifiedEnrollments()
	a, b := v.Enrollments[0], v.Enrollments[1]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, a, key, 200)
	f.probe(v, b, key, 200)
	var own, other, wrongExpiry string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT id::text FROM account_allowance_windows WHERE account_id=$1`, a.AccountID).Scan(&own); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT id::text FROM account_allowance_windows WHERE account_id=$1`, b.AccountID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	// A stray flagged window on the same account is not the reviewed request's
	// fixed grant. Another concurrent enrollment also has its own spare window.
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,pairing_verification) SELECT tenant_id,account_id,starts_at,ends_at+interval '1 hour',unit,allowance,pace_model,true FROM account_allowance_windows WHERE id=$1 RETURNING id::text`, own).Scan(&wrongExpiry); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_allowance_windows SET pairing_verification=false WHERE id=$1`, own); err != nil {
		t.Fatal(err)
	}
	routeWithUnits(t, f, v, a, key, *a.VerificationRunID, map[string]int{"requests": 1}, 409)
	assertWindowLedger(t, f, own, 0, 0)
	assertWindowLedger(t, f, other, 0, 0)
	assertWindowLedger(t, f, wrongExpiry, 0, 0)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_allowance_windows SET pairing_verification=true WHERE id=$1`, own); err != nil {
		t.Fatal(err)
	}
	got := routeWithUnits(t, f, v, a, key, *a.VerificationRunID, map[string]int{"requests": 1}, 200)
	if len(got.Reservations) != 1 || got.Reservations[0].WindowID != own {
		t.Fatal("verification did not bind its own reviewed window")
	}
	assertWindowLedger(t, f, other, 0, 0)
	assertWindowLedger(t, f, wrongExpiry, 0, 0)
	// A queued replay cannot silently accept a reservation moved to a different
	// request's window. Seed the corrupt row to exercise the fail-closed guard.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_reservations SET window_id=$2 WHERE id=$1`, got.Reservations[0].ReservationID, wrongExpiry); err != nil {
		t.Fatal(err)
	}
	routeWithUnits(t, f, v, a, key, *a.VerificationRunID, map[string]int{"requests": 1}, 409)
}
