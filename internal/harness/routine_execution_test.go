// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: agent/bearer consent, stale revisions or forged qualification can
// enable launch; disabling must preserve an unconfirmed existing process.
func TestRoutineExecutionSettingsRequirePersonAndExactQualification(t *testing.T) {
	f := fixture(t)
	f.person.BrowserSession = true
	f.foreign.BrowserSession = true
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,$2,$3,'{}',$3)`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	runtime := modelregistry.ExecutionRuntime{ServerDigest: strings.Repeat("a", 64), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), Capabilities: []string{"routine_native_coding_v1"}, HostMappingDigest: strings.Repeat("d", 64), BudgetModes: []string{"off"}}
	reader := func(ctx context.Context, tx pgx.Tx, _ string) (modelregistry.ExecutionRuntime, error) {
		out := runtime
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt)
		return out, err
	}
	f.mux = http.NewServeMux()
	harness.NewWithLeadAdmission(f.db.App, readyLeadChecks).(*harness.Module).WithRoutineExecutionRuntime(reader).Mount(f.mux)
	path := "/api/projects/" + f.project + "/routine-execution-settings"
	request := func(p tenant.Principal, rev int, enabled bool, id any, status int) map[string]any {
		t.Helper()
		w := f.call(p, "PUT", path, map[string]any{"expected_revision": rev, "automatic_launch_enabled": enabled, "qualification_id": id}, "")
		expect(t, w, status)
		return decode(t, w)
	}
	if got := decode(t, f.call(f.person, "GET", path, nil, "")); got["automatic_launch_enabled"] != false || got["revision"] != float64(0) {
		t.Fatal(got)
	}
	request(f.agent, 0, true, uid(), 403)
	bearer := f.person
	bearer.BrowserSession = false
	request(bearer, 0, true, uid(), 403)
	// Foreign project identities stay hidden by the existing tenant RLS.
	request(f.foreign, 0, true, uid(), 404)
	request(f.person, 0, true, nil, 409)
	request(f.person, 0, true, uid(), 409)
	q := modelregistry.Qualification{ID: uid(), ProjectID: f.project, Runtime: runtime, CoordinatorAcceptance: strings.Repeat("e", 64), OPSAttestation: strings.Repeat("f", 64)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var err error
		q.OwnerPersonID, q.PolicyDigest, err = modelregistry.QualificationPolicyTx(t.Context(), tx, f.person.TenantID, f.project)
		return err
	})
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error { return modelregistry.RecordQualificationTx(t.Context(), tx, f.agent, q) })
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("routine agent recorded qualification: %v", err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error { return modelregistry.RecordQualificationTx(t.Context(), tx, f.person, q) })
	// Neither body fields nor bearer headers can supply acceptance.
	w := f.call(f.person, "PUT", path, map[string]any{"expected_revision": 0, "automatic_launch_enabled": true, "qualification_id": q.ID, "ops_attestation": q.OPSAttestation}, "")
	expect(t, w, 400)
	req := httptest.NewRequest("PUT", path, strings.NewReader(`{"expected_revision":0,"automatic_launch_enabled":true,"qualification_id":"`+q.ID+`"}`))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), f.person))
	req.Header.Set("Authorization", "Bearer synthetic-not-a-credential")
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	expect(t, out, 403)
	got := request(f.person, 0, true, q.ID, 200)
	if got["automatic_launch_enabled"] != true || got["revision"] != float64(1) {
		t.Fatal(got)
	}
	// A configured admission adapter and persisted consent cannot stand in for
	// independent runtime facts on a different/unconfigured executable.
	unconfigured := http.NewServeMux()
	harness.NewWithLeadAdmission(f.db.App, readyLeadChecks).Mount(unconfigured)
	requestOff := httptest.NewRequest("GET", path, nil)
	requestOff = requestOff.WithContext(tenant.WithPrincipal(requestOff.Context(), f.person))
	responseOff := httptest.NewRecorder()
	unconfigured.ServeHTTP(responseOff, requestOff)
	expect(t, responseOff, 200)
	if disabled := decode(t, responseOff); disabled["automatic_launch_enabled"] != false || disabled["wait_reason"] != "runtime_unavailable" {
		t.Fatal(disabled)
	}
	request(f.person, 0, true, q.ID, 409)
	lead := startLead(t, f, 0)
	if lead["automatic_launch_enabled"] != true {
		t.Fatalf("qualified lead projection stayed forced false: %v", lead)
	}
	session, lease, _ := leadCandidate(t, f)
	lead = claimLead(t, f, session, lease, lead["revision"])
	if lead["process_active"] != true {
		t.Fatal("fixture lacks unconfirmed process", lead)
	}
	runtime.ServerDigest = strings.Repeat("0", 64)
	if got := decode(t, f.call(f.person, "GET", path, nil, "")); got["automatic_launch_enabled"] != false || got["consent_enabled"] != true || got["wait_reason"] != "qualification_runtime_changed" {
		t.Fatalf("changed artifact stayed enabled: %v", got)
	}
	request(f.person, 1, true, q.ID, 409)
	runtime = q.Runtime
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_execution_qualifications SET coordinator_acceptance='' WHERE id=$1`, q.ID)
		return err
	})
	got = request(f.person, 1, true, q.ID, 409)
	if !strings.Contains(got["error"].(string), "qualification_unaccepted") {
		t.Fatal(got)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_execution_qualifications SET coordinator_acceptance=$2 WHERE id=$1`, q.ID, q.CoordinatorAcceptance)
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_lead_settings SET revision=revision+1 WHERE project_id=$1`, f.project)
		return err
	})
	got = request(f.person, 1, true, q.ID, 409)
	if !strings.Contains(got["error"].(string), "qualification_policy_changed") {
		t.Fatal(got)
	}
	got = request(f.person, 1, false, nil, 200)
	if got["automatic_launch_enabled"] != false || got["revision"] != float64(2) {
		t.Fatal(got)
	}
	lead = decode(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/lead", nil, ""))
	if lead["process_active"] != true || lead["session_id"] != session {
		t.Fatalf("disable invented exit or changed ownership: %v", lead)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND kind='stop'`, session).Scan(&n)
		if n != 0 {
			t.Fatal("consent handler launched process control")
		}
		return err
	})
}
