// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
)

func retryErrorCode(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Code != want {
		t.Fatalf("retry failure must be %s, got %s", want, out.Code)
	}
}

func retryPath(v agentpairing.View, account string) string {
	return "/api/agent-pairing/computers/" + *v.ComputerID + "/enrollments/" + account + "/verify"
}
func retryBody(v agentpairing.View, e agentpairing.Enrollment) map[string]any {
	return map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": e.VerificationRunID}
}

func TestVerifyAgainKeepsEnrollmentAndSiblingAndCreatesOneBoundRun(t *testing.T) {
	f := newFixture(t)
	p, v := f.twoQualifiedEnrollments()
	e, sibling := v.Enrollments[0], v.Enrollments[1]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	// Reserved but not claimed: retry must release only this account's hold.
	f.reserve(v, e, key, 200)
	identity := func() string {
		t.Helper()
		var value string
		err := f.db.Admin.QueryRow(t.Context(), `SELECT jsonb_build_array(e.account_id,e.computer_id,e.request_id,e.state,e.local_cleanup,e.ongoing_approved_at,a.account_key,a.last_daemon_generation,c.principal_id,c.key_id,c.daemon_id,c.revision,c.local_cleanup,c.local_processes,q.state,q.request_digest,k.revoked_at)::text
		FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.id=e.account_id JOIN agent_pairing_computers c ON c.id=e.computer_id JOIN agent_pairing_requests q ON q.id=e.request_id JOIN agent_keys k ON k.id=c.key_id WHERE e.account_id=$1`, e.AccountID).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := identity()
	var out struct {
		AccountID string    `json:"account_id"`
		RunID     string    `json:"run_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	decodeResult(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 200), &out)
	if out.AccountID != e.AccountID || out.RunID == *e.VerificationRunID || out.ExpiresAt.IsZero() {
		t.Fatal("retry did not create a fresh bound check")
	}
	if identity() != before {
		t.Fatal("retry changed pairing, key, generation, approval or cleanup")
	}
	// A lost response or parallel click cannot create a second run.
	retryErrorCode(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 409), "conflict")
	var newView agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &newView)
	if *newView.Enrollments[1].VerificationRunID != *sibling.VerificationRunID || newView.Enrollments[1].VerificationState != "queued" || *newView.Enrollments[0].VerificationRunID != out.RunID || newView.Enrollments[0].VerificationState != "queued" {
		t.Fatal("retry changed sibling or failed to reset target expiry")
	}
	var runs, windows, audits int
	var status, hold string
	var used int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_runs WHERE requested_account_id=$1),(SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND pairing_verification AND ends_at>clock_timestamp()),(SELECT count(*) FROM events WHERE type='agent_pairing.verification_created' AND after->>'run_id'=$2)`, e.AccountID, out.RunID).Scan(&runs, &windows, &audits); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || windows != 1 || audits != 1 {
		t.Fatalf("retry counts runs=%d live allowances=%d audits=%d", runs, windows, audits)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,res.state,w.reserved FROM agent_runs r JOIN account_reservations res ON res.run_id=r.id JOIN account_allowance_windows w ON w.id=res.window_id WHERE r.id=$1`, *e.VerificationRunID).Scan(&status, &hold, &used); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || hold != "released" || used != 0 {
		t.Fatal("superseded queued check leaked a hold")
	}
	e = newView.Enrollments[0]
	ids := f.reserve(v, e, key, 200)
	f.claim(v, e, key, ids, 200)
	// Claimed work cannot be replaced, even if its approval later expires.
	expireVerification(t, f, e)
	retryErrorCode(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 409), "verification_active")
}

func TestVerifyAgainExpiredReadyProjectionAndOwnerBoundary(t *testing.T) {
	f := newFixture(t)
	p, v := f.twoQualifiedEnrollments()
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, v.Enrollments[1], key, 200)
	expireVerification(t, f, e)
	var listed agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &listed)
	if listed.Enrollments[0].VerificationState != "expired" || listed.Enrollments[0].VerificationExpiredReady {
		t.Fatal("expired account borrowed its sibling's fresh probe")
	}
	f.probe(v, e, key, 200)
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &listed)
	if !listed.Enrollments[0].VerificationExpiredReady || !listed.Enrollments[0].CanVerify {
		t.Fatal("ready expired account did not expose owner recovery")
	}
	// Runtime, missing auth and cross-origin callers cannot replenish authority.
	f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), false, key, 403)
	f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), false, "", 401)
	r := f.request("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "")
	r.Header.Set("Origin", "https://foreign.test")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin retry accepted")
	}
	// Account owner takes precedence over the original pairing approver.
	var other string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other owner','{}') RETURNING id::text`, f.tenantID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2, linked_at=clock_timestamp() WHERE id=$1`, e.AccountID, other); err != nil {
		t.Fatal(err)
	}
	w = f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 403)
	retryErrorCode(t, w, "forbidden")
	var runs int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs WHERE requested_account_id=$1`, e.AccountID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatal("denied retry wrote a run")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2, linked_at=clock_timestamp() WHERE id=$1`, e.AccountID, f.person); err != nil {
		t.Fatal(err)
	}
	f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 200)
}

func TestVerifyAgainConnectOnlyPreservesOrdinaryAllowanceAndTenantBoundary(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	e := v.Enrollments[0]
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/windows", map[string]any{"starts_at": time.Now(), "ends_at": time.Now().Add(time.Hour), "unit": "requests", "allowance": 2, "pace_model": "unrestricted"}, true, "", 201)
	ordinary := func() string {
		t.Helper()
		var data string
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(w) ORDER BY id)::text FROM account_allowance_windows w WHERE account_id=$1 AND NOT pairing_verification`, e.AccountID).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := ordinary()
	f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 200)
	retryErrorCode(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 409), "conflict")
	if ordinary() != before {
		t.Fatal("verification changed ordinary allowance")
	}
	foreign := newFixtureInTenant(t, f.db, "foreign-retry")
	other := foreign.propose("claude")
	foreign.approve(other, "connect_only")
	otherView := foreign.redeem(other)
	retryErrorCode(t, f.call("POST", retryPath(otherView, otherView.Enrollments[0].AccountID), retryBody(otherView, otherView.Enrollments[0]), true, "", 404), "not_found")
	var runs int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs WHERE requested_account_id=$1`, otherView.Enrollments[0].AccountID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatal("cross-tenant retry created a run")
	}
}
