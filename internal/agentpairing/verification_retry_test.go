// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
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

func TestVerifyAgainOwnerWithoutProjectVisibility(t *testing.T) {
	for _, mode := range []string{"connect_only", "one_per_harness"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			p := f.propose("claude")
			f.approve(p, mode)
			v := f.redeem(p)
			e := v.Enrollments[0]
			var role string
			err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'verification_owner','Account manager only') RETURNING id::text`, f.tenantID).Scan(&role); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'account.manage')`, f.tenantID, role); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.person, role)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			// Read through the restricted application role, never the admin pool:
			// the owner cannot see any project before or after the internal write.
			assertHidden := func() {
				t.Helper()
				actor := tenant.Principal{ID: f.person, TenantID: f.tenantID, Kind: tenant.Person}
				err := db.InTenant(tenant.WithPrincipal(t.Context(), actor), f.db.App, f.tenantID, func(tx pgx.Tx) error {
					var visible int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes`).Scan(&visible); err != nil {
						return err
					}
					if visible != 0 {
						t.Fatalf("account-only owner sees %d nodes", visible)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			assertHidden()
			var out struct {
				AccountID string `json:"account_id"`
				RunID     string `json:"run_id"`
			}
			decodeResult(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 200), &out)
			if out.AccountID != e.AccountID || out.RunID == "" || e.VerificationRunID != nil && out.RunID == *e.VerificationRunID {
				t.Fatal("limited-role owner did not create a fresh bound verification")
			}
			var complete bool
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT EXISTS(
			 SELECT 1 FROM agent_runs r JOIN work_orders w ON w.tenant_id=r.tenant_id AND w.node_id=r.work_order_id
			 JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id
			 JOIN agent_pairing_computers c ON c.tenant_id=n.tenant_id AND c.verification_project_id=n.parent_id
			 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id AND e.verification_run_id=r.id
			 WHERE r.id=$1 AND r.tenant_id=$2 AND r.requested_account_id=$3 AND r.purpose='pairing_verification'
			 AND r.status='queued' AND w.requested_by_principal_id=$4 AND w.assignee_principal_id=c.principal_id
			 AND EXISTS(SELECT 1 FROM work_criteria WHERE work_order_id=w.node_id)
			 AND EXISTS(SELECT 1 FROM account_allowance_windows WHERE account_id=e.account_id AND pairing_verification AND allowance=1 AND ends_at>clock_timestamp())
			 AND EXISTS(SELECT 1 FROM events WHERE tenant_id=r.tenant_id AND type='agent_pairing.verification_created' AND actor_principal_id=$4 AND after->>'run_id'=r.id::text))`, out.RunID, f.tenantID, e.AccountID, f.person).Scan(&complete); err != nil {
				t.Fatal(err)
			}
			if !complete {
				t.Fatal("verification lacks its tenant-bound work order, requester, criteria, allowance or audit")
			}
			if e.VerificationRunID != nil {
				var status string
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT status FROM agent_runs WHERE id=$1`, *e.VerificationRunID).Scan(&status); err != nil || status != "cancelled" {
					t.Fatalf("old queued run was not cancelled: %s, %v", status, err)
				}
			}
			assertHidden()
			// The internal step must not create lasting grants or bypass a later
			// revocation of the permission that authorized it.
			if _, err := f.db.Admin.Exec(t.Context(), `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='account.manage'`, f.tenantID, role); err != nil {
				t.Fatal(err)
			}
			e.VerificationRunID = &out.RunID
			retryErrorCode(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 403), "forbidden")
		})
	}
}

func TestVerifyAgainRequiresExplicitReviewedRun(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	e := v.Enrollments[0]
	if e.VerificationRunID != nil {
		t.Fatal("connect-only fixture already has a verification run")
	}
	snapshot := func() string {
		t.Helper()
		var state string
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(e),
		 (SELECT count(*) FROM agent_runs WHERE requested_account_id=e.account_id),
		 (SELECT count(*) FROM account_allowance_windows WHERE account_id=e.account_id),
		 (SELECT count(*) FROM events WHERE type='agent_pairing.verification_created' AND after->>'account_id'=e.account_id::text))::text
		 FROM agent_pairing_enrollments e WHERE account_id=$1`, e.AccountID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"omitted run", map[string]any{"expected_revision": v.Revision}},
		{"empty run", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": ""}},
		{"invalid UUID", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": "not-a-uuid"}},
		{"numeric run", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": 1}},
		{"boolean run", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": false}},
		{"object run", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": map[string]any{}}},
		{"array run", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": []any{}}},
		{"unknown field", map[string]any{"expected_revision": v.Revision, "expected_verification_run_id": nil, "unexpected": true}},
	} {
		t.Log(tc.name)
		retryErrorCode(t, f.call("POST", retryPath(v, e.AccountID), tc.body, true, "", 400), "invalid_request")
		if snapshot() != before {
			t.Fatal("invalid retry changed enrollment, runs, allowances or audit events")
		}
	}
	// Explicit null acknowledges that the reviewed enrollment has no prior run.
	var out struct {
		AccountID string `json:"account_id"`
		RunID     string `json:"run_id"`
	}
	decodeResult(t, f.call("POST", retryPath(v, e.AccountID), retryBody(v, e), true, "", 200), &out)
	if out.AccountID != e.AccountID || out.RunID == "" {
		t.Fatal("explicit-null retry did not create a verification for the reviewed account")
	}
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

// The daemon's actual DTO and wire order reproduce the release-123 failure.
func TestVerificationTelemetryPrestartBatchAndRetry(t *testing.T) {
	// AEON-1041: the Fable model-only status must also reach a successful
	// terminal check, without manufacturing an app_server_protocol failure.
	for _, name := range []string{"exact_four_event_batch", "rejected_batch_terminal_recovery", "successful_fable_batch"} {
		recovery := name == "rejected_batch_terminal_recovery"
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			p := f.propose("claude")
			f.approve(p, "one_per_harness")
			v := f.redeem(p)
			e := v.Enrollments[0]
			key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
			readSettlement := func(want bool) {
				t.Helper()
				var read agentruns.Run
				decodeResult(t, f.call("GET", "/api/runs/"+*e.VerificationRunID, nil, false, key, 200), &read)
				if read.ReservationsSettled == nil || *read.ReservationsSettled != want {
					t.Fatalf("wrong reservation proof: %+v, want %t", read.ReservationsSettled, want)
				}
			}
			readSettlement(false) // No reservations is not a settled reservation.
			f.probe(v, e, key, 200)
			f.claim(v, e, key, f.reserve(v, e, key, 200), 200)
			readSettlement(false) // Active reservations remain unconfirmed.
			batch := []agentd.Telemetry{
				{Sequence: 1, Kind: "status", EffectiveModel: "claude-fable-5-1", ModelEvidence: "vendor_reported"},
				{Sequence: 2, Kind: "turn", TurnCountDelta: 1},
				{Sequence: 3, Kind: "started", Status: "running"},
				{Sequence: 4, Kind: "finished", Status: "failed", ErrorCode: "app_server_protocol"},
			}
			post := func(report agentd.Telemetry, code int) agentruns.Run {
				t.Helper()
				r := f.request("POST", "/api/runs/"+*e.VerificationRunID+"/telemetry", report, false, key)
				r.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
				r.Header.Set(agentruns.GenerationHeader, "test-generation")
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, r)
				if w.Code != code {
					t.Fatalf("sequence %d: HTTP %d, want %d: %s", report.Sequence, w.Code, code, w.Body.String())
				}
				var run agentruns.Run
				if code == 200 {
					decodeResult(t, w, &run)
				}
				return run
			}
			count, turns, errorCode := 4, 1, "app_server_protocol"
			terminalStatus := "failed"
			if name == "successful_fable_batch" {
				terminalStatus, errorCode = "completed", ""
				batch[3].Status, batch[3].ErrorCode = terminalStatus, errorCode
			}
			if recovery {
				var logs bytes.Buffer
				old := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
				defer slog.SetDefault(old)
				bad := batch[0]
				bad.EffectiveModel = "PRIVATE-FIXTURE model\nvalue"
				for i := 0; i < 3; i++ {
					post(bad, 400)
				}
				if !strings.Contains(logs.String(), "effective_model must be a bounded model identifier") || strings.Contains(logs.String(), "PRIVATE-FIXTURE") || strings.Contains(logs.String(), key) {
					t.Fatal("rejection log lost the field-level reason or leaked request values")
				}
				// Fresh sequence, no disputed usage/model/tier and no replay rewrite.
				batch = []agentd.Telemetry{{Sequence: 5, Kind: "finished", Status: "failed", ErrorCode: "reporter_unavailable"}}
				count, turns, errorCode = 1, 0, "reporter_unavailable"
			}
			for i, report := range batch {
				run := post(report, 200)
				if !recovery && i < 2 && run.Status != "starting" {
					t.Fatal("model/turn event invented completed startup")
				}
				if !recovery && (run.EffectiveModel == nil || *run.EffectiveModel != "claude-fable-5-1" || run.ModelEvidence != "vendor_reported") {
					t.Fatal("vendor model evidence lost")
				}
			}
			// Lost HTTP receipts can replay the original reports after termination.
			for _, report := range batch {
				post(report, 200)
			}
			var n, gotTurns, held int
			var status, code string
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,
    (SELECT count(*) FROM run_telemetry WHERE run_id=r.id),
    (SELECT coalesce(sum(turn_count_delta),0) FROM run_telemetry WHERE run_id=r.id),
    (SELECT coalesce(error_code,'') FROM run_telemetry WHERE run_id=r.id ORDER BY sequence DESC LIMIT 1),
    (SELECT count(*) FROM account_reservations WHERE run_id=r.id AND state='active')
    FROM agent_runs r WHERE r.id=$1`, *e.VerificationRunID).Scan(&status, &n, &gotTurns, &code, &held); err != nil {
				t.Fatal(err)
			}
			if status != terminalStatus || n != count || gotTurns != turns || code != errorCode || held != 0 {
				t.Fatalf("wrong settlement: %s rows=%d turns=%d code=%s holds=%d", status, n, gotTurns, code, held)
			}
			readSettlement(true)
			// A second unsettled reservation must defeat a settled first row.
			var extraReservation string
			if err := f.db.Admin.QueryRow(t.Context(), `WITH extra_window AS (
 INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance)
 SELECT tenant_id,account_id,clock_timestamp(),clock_timestamp()+interval '1 hour','requests',1
 FROM account_allowance_windows WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1 LIMIT 1)
 RETURNING tenant_id,id
 ) INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units)
 SELECT tenant_id,$1,id,1 FROM extra_window RETURNING id::text`, *e.VerificationRunID).Scan(&extraReservation); err != nil {
				t.Fatal(err)
			}
			readSettlement(false)
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_reservations SET state='settled',actual_units=0,settled_at=clock_timestamp() WHERE id=$1`, extraReservation); err != nil {
				t.Fatal(err)
			}
			readSettlement(true)
			// Retain the fixture row and independently vary reservation state:
			// terminal status and released capacity must not count as settlement.
			for _, reservationState := range []string{"active", "released", "settled"} {
				if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_reservations SET state=$2,
 settled_at=CASE WHEN $2='active' THEN NULL ELSE clock_timestamp() END,
 actual_units=CASE WHEN $2='settled' THEN 0 ELSE NULL END WHERE run_id=$1`, *e.VerificationRunID, reservationState); err != nil {
					t.Fatal(err)
				}
				readSettlement(reservationState == "settled")
			}
			var current agentpairing.View
			decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &current)
			result := current.Enrollments[0]
			if result.VerificationState != terminalStatus || result.VerificationError != errorCode || result.VerificationStalled || !result.CanVerify {
				t.Fatal("failed verification missing from owner projection")
			}
			var retried struct {
				RunID string `json:"run_id"`
			}
			decodeResult(t, f.call("POST", retryPath(current, e.AccountID), retryBody(current, result), true, "", 200), &retried)
			if retried.RunID == "" || retried.RunID == *e.VerificationRunID {
				t.Fatal("settled failure cannot be retried")
			}
		})
	}
}

func TestStalledVerificationPreservesOwnership(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	f.claim(v, e, key, f.reserve(v, e, key, 200), 200)
	for _, tc := range []struct {
		age     string
		stalled bool
	}{{"30 seconds", false}, {"121 seconds", true}} {
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET verification_claimed_at=clock_timestamp()-$2::interval WHERE account_id=$1`, e.AccountID, tc.age); err != nil {
			t.Fatal(err)
		}
		var current agentpairing.View
		decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &current)
		got := current.Enrollments[0]
		if got.VerificationStalled != tc.stalled || got.VerificationState != "starting" || got.AccountingState != "unconfirmed" || len(got.ActiveRunIDs) != 1 {
			t.Fatal("timeout concealed active ownership or missed the bounded wait")
		}
		if tc.stalled && got.VerificationError != "verification_timeout" {
			t.Fatal("stalled check lacks its reason")
		}
		retryErrorCode(t, f.call("POST", retryPath(current, e.AccountID), retryBody(current, got), true, "", 409), "verification_active")
	}
	var status string
	var held int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,(SELECT count(*) FROM account_reservations WHERE run_id=r.id AND state='active') FROM agent_runs r WHERE r.id=$1`, *e.VerificationRunID).Scan(&status, &held); err != nil {
		t.Fatal(err)
	}
	if status != "starting" || held != 1 {
		t.Fatal("elapsed time granted process-exit or accounting authority")
	}
}

func TestVerificationErrorOnlyStatusKeepsCauseAndClaimState(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	f.claim(v, e, key, f.reserve(v, e, key, 200), 200)
	path := "/api/runs/" + *e.VerificationRunID + "/telemetry"
	post := func(report agentd.Telemetry, want int) agentruns.Run {
		t.Helper()
		r := f.request("POST", path, report, false, key)
		r.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
		r.Header.Set(agentruns.GenerationHeader, "test-generation")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("sequence %d: HTTP %d, want %d: %s", report.Sequence, w.Code, want, w.Body.String())
		}
		var run agentruns.Run
		if want == 200 {
			decodeResult(t, w, &run)
		} else if want == 400 {
			reason := "status report requires status, effective_model or error_code"
			if report.ErrorCode != "" {
				reason = "invalid error code"
			}
			var rejected struct {
				Error string `json:"error"`
			}
			decodeResult(t, w, &rejected)
			if rejected.Error != reason {
				t.Fatalf("rejected for wrong reason: %s", rejected.Error)
			}
		}
		return run
	}
	// Exact status event from protocol.go when the bounded scanner fails.
	report := agentd.Telemetry{Sequence: 1, Kind: "status", ErrorCode: "event_stream_bound"}
	for i := 0; i < 2; i++ {
		run := post(report, 200)
		if run.Status != "starting" || run.EffectiveModel != nil || run.InputTokens != 0 || run.EndedAt != nil {
			t.Fatal("error evidence invented startup, model or usage")
		}
	}
	var cause string
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*),min(error_code) FROM run_telemetry WHERE run_id=$1`, *e.VerificationRunID).Scan(&count, &cause); err != nil || count != 1 || cause != "event_stream_bound" {
		t.Fatalf("error cause or replay lost: %d %s %v", count, cause, err)
	}
	post(agentd.Telemetry{Sequence: 2, Kind: "status"}, 400)
	post(agentd.Telemetry{Sequence: 2, Kind: "status", ErrorCode: "private-unknown-error"}, 400)
	run := post(agentd.Telemetry{Sequence: 2, Kind: "finished", Status: "failed", ErrorCode: "event_stream_bound"}, 200)
	if run.Status != "failed" || run.EndedAt == nil {
		t.Fatal("stream error did not settle truthfully")
	}
	var listed agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &listed)
	if listed.Enrollments[0].VerificationError != "event_stream_bound" {
		t.Fatal("verification lost original failure cause")
	}
}

func TestVerificationRejectionLogsRunAndProtocolConflicts(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	f.claim(v, e, key, f.reserve(v, e, key, 200), 200)
	post := func(report agentd.Telemetry, want int) {
		t.Helper()
		r := f.request("POST", "/api/runs/"+*e.VerificationRunID+"/telemetry", report, false, key)
		r.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
		r.Header.Set(agentruns.GenerationHeader, "test-generation")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("HTTP %d, want %d: %s", w.Code, want, w.Body.String())
		}
	}
	post(agentd.Telemetry{Sequence: 2, Kind: "started"}, 200)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)
	for _, tc := range []struct {
		report agentd.Telemetry
		status int
		reason string
	}{
		{agentd.Telemetry{Sequence: 2, Kind: "started", Status: "running"}, 409, "divergent telemetry replay"},
		{agentd.Telemetry{Sequence: 1, Kind: "heartbeat"}, 409, "telemetry sequence is not monotonic"},
		{agentd.Telemetry{Sequence: 3, Kind: "status", Status: "starting"}, 409, "run cannot return to starting"},
		{agentd.Telemetry{Sequence: 3, Kind: "status", EffectiveModel: "PRIVATE-FIXTURE model"}, 400, "effective_model must be a bounded model identifier"},
		{agentd.Telemetry{Sequence: 3, Kind: "PRIVATE-FIXTURE kind"}, 400, "invalid telemetry kind"},
	} {
		logs.Reset()
		post(tc.report, tc.status)
		var line struct {
			RunID    string `json:"run_id"`
			Kind     string `json:"kind"`
			Sequence int64  `json:"sequence"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(logs.Bytes(), &line); err != nil || line.RunID != *e.VerificationRunID || line.Reason != tc.reason {
			t.Fatalf("missing run or fixed rejection reason: %v", err)
		}
		wantKind := tc.report.Kind
		if tc.reason == "invalid telemetry kind" {
			wantKind = ""
		}
		if line.Kind != wantKind || line.Sequence != tc.report.Sequence {
			t.Fatal("missing bounded kind or sequence")
		}
		if strings.Contains(logs.String(), "PRIVATE-FIXTURE") || strings.Contains(logs.String(), key) {
			t.Fatal("diagnostics leaked request values")
		}
	}
}
