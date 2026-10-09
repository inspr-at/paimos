// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func ledgerCall(t *testing.T, f *fixture, method, path string, body any, key, generation string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := f.request(method, path, body, false, key)
	if generation != "" {
		r.Header.Set(agentpairing.LedgerGenerationHeader, generation)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}

func enrollLedger(t *testing.T, f *fixture, key, generation string) agentpairing.View {
	t.Helper()
	var v agentpairing.View
	decodeResult(t, ledgerCall(t, f, "POST", "/api/agent-pairing/self/ledger", map[string]string{"generation": generation}, key, "", 200), &v)
	if !v.LedgerMode || v.LedgerGeneration == nil || *v.LedgerGeneration != generation || v.LedgerEnrolledAt == nil || !slices.Contains(v.ServerCapabilities, agentpairing.LedgerCapability) {
		t.Fatal("enrolment response is not persisted ledger state")
	}
	return v
}

func ledgerProposal(t *testing.T, f *fixture, harness string, capable bool, parent *proposal, computer *agentpairing.View) *proposal {
	t.Helper()
	p := &proposal{id: uuid(t, f.db), device: nonce(), runtime: nonce(), lifecycle: nonce()}
	caps := []string{"managed_runs"}
	if capable {
		caps = append(caps, agentpairing.LedgerCapability)
	}
	p.request = map[string]any{"request_id": p.id, "tenant_id": f.tenantID, "device_hash": hash(p.device), "runtime_hash": hash(p.runtime), "lifecycle_hash": hash(p.lifecycle), "computer_name": "Ledger workstation", "platform": "darwin", "arch": "arm64", "workspace_path": "/tmp/pairing-fixture", "capabilities": caps, "accounts": []map[string]string{{"account_key": harness + "-" + p.id, "harness": harness, "label": "Ledger account", "model_profile_id": f.profiles[harness]}}}
	if parent != nil {
		p.runtime, p.lifecycle = parent.runtime, parent.lifecycle
		p.request["runtime_hash"], p.request["lifecycle_hash"] = hash(p.runtime), hash(p.lifecycle)
		p.request["computer_name"] = parent.request["computer_name"]
		p.request["existing_computer_id"], p.request["existing_lifecycle_secret"] = *computer.ComputerID, p.lifecycle
	}
	f.submit(p)
	return p
}

func ledgerRouteBody(v agentpairing.View, e agentpairing.Enrollment) map[string]any {
	return map[string]any{"run_id": *e.VerificationRunID, "daemon_id": *v.DaemonID, "account_ids": []string{e.AccountID}, "estimated_units": map[string]int{"requests": 1}}
}

func ledgerClaimBody(v agentpairing.View, ids []string) map[string]any {
	return map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": ids}
}

// Risk: an old daemon, route replay or stale request could start work after the
// tenant requirement or computer generation changes. Retain a real reservation
// so the claim/replay refusal cannot pass merely because routing never worked.
func TestLedgerBoundaryDowngradeReplayRebuildAndVerification(t *testing.T) {
	f := newFixture(t)
	p := ledgerProposal(t, f, "claude", true, nil, nil)
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	e := v.Enrollments[0]
	f.probe(v, e, key, 200)
	ids := f.reserve(v, e, key, 200) // Legacy path remains available before activation.
	generation := uuid(t, f.db)
	v = enrollLedger(t, f, key, generation)
	retry := enrollLedger(t, f, key, generation)
	if retry.Revision != v.Revision || !retry.LedgerEnrolledAt.Equal(*v.LedgerEnrolledAt) {
		t.Fatal("identical enrolment retry rewrote state")
	}
	var activated bool
	var events int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT enforced_at IS NOT NULL AND ledger_mode_at IS NOT NULL FROM account_use_rules WHERE tenant_id=$1`, f.tenantID).Scan(&activated); err != nil || !activated {
		t.Fatal("enrolment did not activate the rollback floor")
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='agent_pairing.ledger_enrolled'`, f.tenantID).Scan(&events); err != nil || events != 1 {
		t.Fatal("enrolment audit missing or duplicated")
	}
	for _, header := range []string{"", uuid(t, f.db)} {
		assertLedgerBlocked(t, f, v, e, key, header, ids)
	}
	var queued []agentruns.Run
	decodeResult(t, ledgerCall(t, f, "GET", "/api/runs/queued", nil, key, generation, 200), &queued)
	if len(queued) != 1 || queued[0].ID != *e.VerificationRunID {
		t.Fatal("matching generation did not expose verification")
	}
	ledgerCall(t, f, "POST", "/api/agent-accounts/route", ledgerRouteBody(v, e), key, generation, 200)
	newGeneration := uuid(t, f.db)
	enrollLedger(t, f, key, newGeneration)
	assertLedgerBlocked(t, f, v, e, key, generation, ids)
	ledgerCall(t, f, "POST", "/api/agent-accounts/route", ledgerRouteBody(v, e), key, newGeneration, 200)
	ledgerCall(t, f, "POST", "/api/runs/"+*e.VerificationRunID+"/claim", ledgerClaimBody(v, ids), key, newGeneration, 200)
	// A downgrade can still settle existing work under the telemetry ownership
	// fence. Ledger mode fences new dispatch, not honest completion reporting.
	f.telemetry(v, e, key, 200)
	var modeBefore, modeAfter string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT ledger_mode_at::text FROM account_use_rules WHERE tenant_id=$1`, f.tenantID).Scan(&modeBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_use_rules SET ledger_mode_at=NULL WHERE tenant_id=$1`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT ledger_mode_at::text FROM account_use_rules WHERE tenant_id=$1`, f.tenantID).Scan(&modeAfter); err != nil || modeAfter != modeBefore {
		t.Fatal("ledger mode was reset")
	}
}

func assertLedgerBlocked(t *testing.T, f *fixture, v agentpairing.View, e agentpairing.Enrollment, key, header string, ids []string) {
	t.Helper()
	var queued []agentruns.Run
	decodeResult(t, ledgerCall(t, f, "GET", "/api/runs/queued", nil, key, header, 200), &queued)
	if len(queued) != 0 {
		t.Fatal("blocked daemon saw queued work")
	}
	for _, req := range []struct {
		path string
		body any
	}{
		{"/api/agent-accounts/route", ledgerRouteBody(v, e)},
		{"/api/runs/" + *e.VerificationRunID + "/claim", ledgerClaimBody(v, ids)},
	} {
		w := ledgerCall(t, f, "POST", req.path, req.body, key, header, 409)
		if !strings.Contains(w.Body.String(), "ledger_enrollment_required") {
			t.Fatal("refused for a different reason: " + w.Body.String())
		}
	}
	var state string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT status FROM agent_runs WHERE id=$1`, *e.VerificationRunID).Scan(&state); err != nil || state != "queued" {
		t.Fatal("blocked claim mutated run")
	}
}

// Risk: approval followed by a helper crash leaves a fresh/replacement/Add
// harness computer open to a downgrade. The tenant requirement must already
// cover it, including a header supplied before any generation was enrolled.
func TestLedgerBoundaryApprovalCrashBeforeEnrollment(t *testing.T) {
	for _, scenario := range []string{"fresh", "replacement", "add_harness"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			p := ledgerProposal(t, f, "codex", true, nil, nil)
			f.approve(p, "connect_only")
			v := f.redeem(p)
			key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
			generation := uuid(t, f.db)
			enrollLedger(t, f, key, generation)
			if scenario == "replacement" {
				f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
			}
			var q *proposal
			if scenario == "add_harness" {
				q = ledgerProposal(t, f, "claude", true, p, &v)
			} else {
				q = ledgerProposal(t, f, "claude", true, nil, nil)
			}
			approved := f.approve(q, "one_per_harness")
			if !approved.LedgerMode || approved.LedgerGeneration != nil {
				t.Fatal("approval left a usable ledger enrollment")
			}
			v = f.redeem(q)
			key = "aeon_" + v.RuntimePrefix + "_" + q.runtime
			var e agentpairing.Enrollment
			for _, candidate := range v.Enrollments {
				if candidate.VerificationRunID != nil {
					e = candidate
				}
			}
			if e.VerificationRunID == nil {
				t.Fatal("verification fixture missing")
			}
			f.probe(v, e, key, 200)
			// A valid UUID satisfies claim body decoding without inventing a route.
			ids := []string{uuid(t, f.db)}
			for _, header := range []string{"", generation} {
				assertLedgerBlocked(t, f, v, e, key, header, ids)
			}
			// Old helper re-provisioning and an installed-service downgrade only
			// report lifecycle state. Neither can set an enrolled generation.
			f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": q.id, "lifecycle_secret": q.lifecycle, "progress": map[string]string{"state": "provisioning"}}, false, "", 200)
			f.rebuildHandler()
			assertLedgerBlocked(t, f, v, e, key, "", ids)
			newGeneration := uuid(t, f.db)
			enrollLedger(t, f, key, newGeneration)
			var route agentaccounts.RouteResult
			decodeResult(t, ledgerCall(t, f, "POST", "/api/agent-accounts/route", ledgerRouteBody(v, e), key, newGeneration, 200), &route)
			ledgerCall(t, f, "POST", "/api/runs/"+*e.VerificationRunID+"/claim", ledgerClaimBody(v, reservationIDs(route)), key, newGeneration, 200)
		})
	}
}

func TestLedgerBoundaryApprovalRequiresCapability(t *testing.T) {
	for _, scenario := range []string{"fresh", "replacement", "add_harness"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			p := ledgerProposal(t, f, "codex", true, nil, nil)
			f.approve(p, "connect_only")
			v := f.redeem(p)
			enrollLedger(t, f, "aeon_"+v.RuntimePrefix+"_"+p.runtime, uuid(t, f.db))
			if scenario == "replacement" {
				f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
			}
			var q *proposal
			if scenario == "add_harness" {
				q = ledgerProposal(t, f, "claude", false, p, &v)
			} else {
				q = ledgerProposal(t, f, "claude", false, nil, nil)
			}
			w := f.call("POST", "/api/agent-pairing/requests/"+q.id+"/approve", map[string]any{"request_digest": q.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{q.review.Requested[0].AccountKey}}, true, "", 409)
			if !strings.Contains(w.Body.String(), "ledger_enrollment_required") {
				t.Fatal("wrong approval refusal")
			}
			var state string
			var computer *string
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT state,computer_id::text FROM agent_pairing_requests WHERE id=$1`, q.id).Scan(&state, &computer); err != nil || state != "pending" || computer != nil {
				t.Fatal("refused approval provisioned authority")
			}
		})
	}
}

func TestLedgerBoundaryNeverEnrolledAndClassicDaemon(t *testing.T) {
	f := newFixture(t)
	old := f.propose("claude")
	f.approve(old, "one_per_harness")
	oldView := f.redeem(old)
	oldKey := "aeon_" + oldView.RuntimePrefix + "_" + old.runtime
	e := oldView.Enrollments[0]
	f.probe(oldView, e, oldKey, 200)
	ids := f.reserve(oldView, e, oldKey, 200)
	modern := ledgerProposal(t, f, "codex", true, nil, nil)
	f.approve(modern, "connect_only")
	v := f.redeem(modern)
	enrollLedger(t, f, "aeon_"+v.RuntimePrefix+"_"+modern.runtime, uuid(t, f.db))
	var lifecycle agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/self", nil, false, oldKey, 200), &lifecycle)
	if !lifecycle.LedgerMode || lifecycle.LedgerGeneration != nil {
		t.Fatal("existing never-enrolled computer not told to enroll")
	}
	assertLedgerBlocked(t, f, oldView, e, oldKey, "", ids)
	// Existing computers do not need ledger-v1 in their historic request: a new
	// binary imports and enrolls automatically through the same authenticated API.
	generation := uuid(t, f.db)
	enrollLedger(t, f, oldKey, generation)
	ledgerCall(t, f, "POST", "/api/runs/"+*e.VerificationRunID+"/claim", ledgerClaimBody(oldView, ids), oldKey, generation, 200)
	var classic struct {
		Token       string `json:"token"`
		PrincipalID string `json:"principal_id"`
	}
	decodeResult(t, f.call("POST", "/api/agent-keys", map[string]any{"name": "Classic daemon fixture", "scopes": []string{"run.read", "run.claim", "account.route"}}, true, "", 201), &classic)
	var order workorders.Order
	decodeResult(t, f.call("POST", "/api/work-orders", map[string]any{"title": "Classic daemon boundary", "criteria": []string{"No dispatch"}, "assignee_principal_id": classic.PrincipalID}, true, "", 201), &order)
	decodeResult(t, f.call("PATCH", "/api/work-orders/"+order.NodeID, map[string]any{"expected_revision": order.Revision, "status": "ready"}, true, "", 200), &order)
	var run agentruns.Run
	decodeResult(t, f.call("POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": classic.PrincipalID, "model_profile_id": f.profiles["claude"]}, true, "", 201), &run)
	var queued []agentruns.Run
	decodeResult(t, ledgerCall(t, f, "GET", "/api/runs/queued", nil, classic.Token, generation, 200), &queued)
	if len(queued) != 0 {
		t.Fatal("classic daemon saw work")
	}
	for _, req := range []struct {
		path string
		body any
	}{
		{"/api/agent-accounts/route", map[string]any{"run_id": run.ID, "daemon_id": "classic", "account_ids": []string{e.AccountID}, "estimated_units": map[string]int{"requests": 1}}},
		{"/api/runs/" + run.ID + "/claim", map[string]any{"daemon_id": "classic", "daemon_generation": "classic-generation", "reservation_ids": []string{uuid(t, f.db)}}},
	} {
		w := ledgerCall(t, f, "POST", req.path, req.body, classic.Token, generation, 409)
		if !strings.Contains(w.Body.String(), "ledger_enrollment_required") {
			t.Fatal("classic refusal used wrong reason")
		}
	}
	f.call("POST", "/api/agent-pairing/self/ledger", map[string]string{"generation": generation}, false, classic.Token, 404)
}

func TestLedgerEnrollmentBoundedInputAndLiveAuthority(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	generation := uuid(t, f.db)
	f.call("POST", "/api/agent-pairing/self/ledger", map[string]string{"generation": generation}, true, "", 403)
	for _, body := range []any{map[string]string{"generation": ""}, map[string]string{"generation": strings.Repeat("x", 129)}, map[string]string{"generation": generation, "computer_id": *v.ComputerID}} {
		f.call("POST", "/api/agent-pairing/self/ledger", body, false, key, 400)
	}
	// Revoke the principal permission after authentication would have passed;
	// RequireTx must read current authority inside the fenced enrollment write.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE permission='run.claim' AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1)`, *v.PrincipalID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-pairing/self/ledger", map[string]string{"generation": generation}, false, key, 403)
	var enrolled bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT ledger_generation IS NOT NULL FROM agent_pairing_computers WHERE id=$1`, *v.ComputerID).Scan(&enrolled); err != nil || enrolled {
		t.Fatal("unauthorized or invalid enrollment wrote state")
	}
}

func TestLedgerBoundaryLifecycleAdvertisement(t *testing.T) {
	f := newFixture(t)
	var guide agentsetup.Guide
	decodeResult(t, f.call("GET", "/api/agent-pairing/guide", nil, false, "", 200), &guide)
	if !slices.Contains(guide.ServerCapabilities, agentpairing.LedgerCapability) {
		t.Fatal("guide did not advertise ledger-v1")
	}
	p := f.propose("claude")
	if !slices.Contains(p.review.ServerCapabilities, agentpairing.LedgerCapability) || p.review.LedgerMode {
		t.Fatal("pending lifecycle advertisement incorrect")
	}
}
