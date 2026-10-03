// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func adoptFixture(t *testing.T) *ticketFixture {
	t.Helper()
	f := ticketSetup(t)
	f.tx(func(tx pgx.Tx) error {
		if _, e := tx.Exec(t.Context(), `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by,next_sequence) VALUES($1,$2,$3,2)`, f.person.TenantID, f.project, f.person.ID); e != nil {
			return e
		}
		_, e := tx.Exec(t.Context(), `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank) VALUES($1,$2,$3,1,'V')`, f.person.TenantID, f.project, f.release)
		return e
	})
	return f
}
func TestContainerHTTPValidationCASAndPersonAuthority(t *testing.T) {
	f := adoptFixture(t)
	base := "/api/projects/" + f.project + "/releases/" + f.release
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", base, "", 200}, {"GET", base + "/items?limit=201", "", 400}, {"GET", base + "/items?cursor=malformed", "", 400},
		{"PATCH", base, `{"expected_revision":1,"entry_closes_at":"bad","build_settings":{}}`, 400},
		{"PATCH", base, `{"expected_revision":1,"title":"Renamed","build_settings":{"budget_agent_hours":1}}`, 403},
		{"PATCH", base, `{"expected_revision":1,"title":"Renamed"}`, 200},
		{"PATCH", base, `{"expected_revision":1,"title":"Stale edit"}`, 409},
		{"POST", base + "/cut", `{"expected_revision":2,"version_scheme":"legacy","version":"1.0","to":"frozen"}`, 400},
		{"POST", base + "/state", `{"expected_revision":2,"to":"building"}`, 200},
	} {
		w := f.request(f.person, tc.method, tc.path, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	var title string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT title FROM nodes WHERE id=$1`, f.release).Scan(&title)
	})
	if title != "Renamed" {
		t.Fatal("denied/stale edit changed title")
	}
	w := f.request(f.agent, "POST", base+"/state", `{"expected_revision":3,"to":"planned"}`)
	if w.Code != 403 {
		t.Fatalf("agent lifecycle returned %d %s", w.Code, w.Body.String())
	}
	w = f.request(f.person, "PATCH", base, `{"expected_revision":3,"entry_closes_at":null}`)
	if w.Code != 409 {
		t.Fatalf("deadline changed after planned: %d", w.Code)
	}
}
func TestContainerPlacementPreservesFlagsAndReturnsDestinationRevision(t *testing.T) {
	f := adoptFixture(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "owner")
	id := f.existing("task", f.project, "Task", "open")
	path := "/api/nodes/" + id + "/ships-in"
	body := fmt.Sprintf(`{"expected_project_id":%q,"expected_revision":0,"release_id":%q,"expected_release_revision":1,"expedite":true,"due_on":"2026-10-20"}`, f.project, f.release)
	w := f.request(f.person, "PUT", path, body)
	if w.Code != 200 {
		t.Fatalf("placement %d %s", w.Code, w.Body.String())
	}
	var result delivery.PlacementResult
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.ReleaseRevision != 2 || len(result.Items) != 1 || !result.Items[0].Expedite {
		t.Fatal("missing captured placement revision")
	}
	body = fmt.Sprintf(`{"expected_project_id":%q,"expected_revision":1,"release_id":null}`, f.project)
	w = f.request(f.person, "PUT", path, body)
	if w.Code != 200 {
		t.Fatalf("unplace %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if !result.Items[0].Expedite || result.Items[0].DueOn == nil || *result.Items[0].DueOn != "2026-10-20" {
		t.Fatal("omitted fields cleared person flags")
	}
}

type reportFake struct {
	calls   int
	project string
}

func (f *reportFake) ReadReport(context.Context, tenant.Principal, string, string, int) (delivery.AdoptionReport, error) {
	return delivery.AdoptionReport{Items: []json.RawMessage{json.RawMessage(`{"reason":"needs repair"}`)}, Revision: 4}, nil
}
func (f *reportFake) RequestTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, action string, revision int64) (delivery.AdoptionJob, error) {
	if revision != 4 {
		return delivery.AdoptionJob{}, delivery.ErrRevisionChanged
	}
	var mode string
	if e := tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only')`).Scan(&mode); e != nil {
		return delivery.AdoptionJob{}, e
	}
	if mode != "off" {
		return delivery.AdoptionJob{}, fmt.Errorf("request did not run in mutation transaction")
	}
	f.calls++
	f.project = project
	return delivery.AdoptionJob{State: "pending", Revision: 5}, nil
}
func TestAdoptionReportingBoundaryAndNoSynchronousApply(t *testing.T) {
	f := ticketSetup(t)
	fake := &reportFake{}
	f.mux = http.NewServeMux()
	New(f.db.App, WithAdoptionReporting(fake)).Mount(f.mux)
	path := "/api/projects/" + f.project + "/delivery/adopt"
	body := `{"action":"retry","expected_revision":4}`
	w := f.request(f.person, "POST", path, body)
	if w.Code != 403 || fake.calls != 0 {
		t.Fatalf("member invoked adoption %d calls=%d", w.Code, fake.calls)
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "owner")
	w = f.request(f.person, "POST", path, body)
	if w.Code != 202 || fake.calls != 1 || fake.project != f.project || !strings.Contains(w.Body.String(), "status_ref") {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	var count int
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM project_delivery WHERE project_node_id=$1`, f.project).Scan(&count)
	})
	if count != 0 {
		t.Fatal("retry synchronously adopted project")
	}
	w = f.request(f.person, "POST", path, `{"action":"retry","expected_revision":3}`)
	if w.Code != 409 || fake.calls != 1 {
		t.Fatalf("stale job request: %d", w.Code)
	}
	w = f.request(f.person, "GET", "/api/projects/"+f.project+"/delivery/adoption-report", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "needs repair") {
		t.Fatalf("persisted report %d %s", w.Code, w.Body.String())
	}
}
