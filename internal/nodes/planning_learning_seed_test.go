// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/jackc/pgx/v5"
)

// All clocks are fixed; the active time deliberately differs from wall time.
func seedLearningTicket(t *testing.T, w planningWorld, index int, active any, provisional bool) nodeJSON {
	t.Helper()
	n := w.node(t, fmt.Sprintf("LEARN-%d", index), "ticket", w.root.ID, "done", nil)
	order := w.node(t, fmt.Sprintf("LEARNORDER-%d", index), "work_order", w.root.ID, "open", nil)
	w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 4_000_000, 0, 0, "api", "")
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(index) * 24 * time.Hour)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'done')`, w.admin.TenantID, order.ID, w.admin.ID); err != nil {
			return err
		}
		var run, session string
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,active_ms,started_at,ended_at)
 SELECT $1,$2,$3,id,'completed',$4,$5::timestamptz,$5::timestamptz+interval '8 hours' FROM model_profiles WHERE slug='codex-astra-xhigh' RETURNING id::text`, w.admin.TenantID, order.ID, w.agent, active, start).Scan(&run); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `UPDATE harness_sessions SET run_id=$2::uuid,model_profile_id=(SELECT id FROM model_profiles WHERE slug='codex-astra-xhigh'),
 created_at=$3::timestamptz,heartbeat_at=$3::timestamptz+interval '8 hours',stopped_at=$3::timestamptz+interval '8 hours',
 work_placement='{"kind":"backend","bucket":"complex","planned_profile_id":null}'::jsonb
 WHERE ticket_node_id=$1::uuid RETURNING id::text`, n.ID, run, start).Scan(&session); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE harness_session_usage SET provisional=$2 WHERE session_id=$1::uuid`, session, provisional); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO ticket_estimate_snapshots(tenant_id,ticket_node_id,source_project_id,started_at,snapshot)
 VALUES($1,$2,$3,$4,'{"estimate_hours":2,"source":"session","rate_basis":{"basis":"default","tickets":0,"tokens_per_hour":5000000}}')`, w.admin.TenantID, n.ID, w.root.ID, start); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO outcome_events(tenant_id,kind,project_id,ticket_node_id,session_id,idempotency_key,actor_principal_id,source,payload,request_digest,recorded_at)
 VALUES($1,'ticket_done',$2,$3::uuid,$4,'learning:'||($3::uuid)::text,$5,'recorded','{}',decode(md5(($3::uuid)::text),'hex'),$6::timestamptz+interval '9 hours')`, w.admin.TenantID, w.root.ID, n.ID, session, w.admin.ID, start)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPlanningLearningSeededActiveTimeSortAndSnapshot(t *testing.T) {
	w := planningSetup(t)
	for i := range 5 {
		seedLearningTicket(t, w, i+1, int64(4*3600000), false)
	}
	seedLearningTicket(t, w, 6, nil, false)
	seedLearningTicket(t, w, 7, int64(4*3600000), true)
	target := placementNode(t, w, "LEARNOPEN-1", map[string]any{"route_role": "build-hard", "area": "backend", "complexity": "L", "estimate_hours": 3})
	other := placementNode(t, w, "LEARNOPEN-2", map[string]any{"route_role": "build-hard", "area": "backend", "complexity": "L", "estimate_hours": 1})
	path := "/api/nodes?within=" + w.root.ID + "&kind=ticket&state=open&sort=-tokens"
	view := planningOf(t, w.admin, path)[target.Key]
	if view == nil || view.Tokens.Calibration.Level != "cell" || view.Tokens.Calibration.Tickets != 5 || view.Tokens.Calibration.TokensPerHour != 1_000_000 || *view.Tokens.Estimated != 6_000_000 {
		t.Fatalf("seeded cell: %+v", view)
	}
	if view.ModelEstimate == nil || *view.ModelEstimate.Hours != 6 || *view.ModelEstimate.SpeedFactor != 2 || view.ModelEstimate.SpeedTickets != 5 {
		t.Fatalf("speed from measured active time: %+v", view.ModelEstimate)
	}
	var profile string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='codex-astra-xhigh'`).Scan(&profile)
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	usagedashboard.New(appPool).Mount(mux)
	for _, tc := range []struct {
		name      string
		principal *tenant.Principal
		query     string
		status    int
	}{
		{"measured", &w.admin, "profile_id=" + profile + "&kind=backend&bucket=complex", 200},
		{"permission", &w.viewer, "profile_id=" + profile + "&kind=backend&bucket=complex", 403},
		{"unauthenticated", nil, "profile_id=" + profile + "&kind=backend&bucket=complex", 401},
		{"bad bucket", &w.admin, "profile_id=" + profile + "&kind=backend&bucket=L", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/usage/model-estimates?"+tc.query, nil)
			if tc.principal != nil {
				r = r.WithContext(tenant.WithPrincipal(r.Context(), *tc.principal))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, r)
			if rec.Code != tc.status {
				t.Fatalf("history %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code == 200 {
				var h usagedashboard.ModelEstimateHistory
				if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
					t.Fatal(err)
				}
				if h.Tickets != 5 || h.Hours == nil || *h.Hours != 4 || h.SpeedFactor == nil || *h.SpeedFactor != 2 {
					t.Fatalf("history %+v", h)
				}
			}
		})
	}
	page := listPage(t, w.admin, path)
	if len(page.Items) != 2 || page.Items[0].ID != target.ID || page.Items[1].ID != other.ID {
		t.Fatal("sort does not use model-adjusted rates", page.Items)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error { return CapturePlanningStart(t.Context(), tx, target.ID, "session") }); err != nil {
		t.Fatal(err)
	}
	view = planningOf(t, w.admin, path)[target.Key]
	if view.Snapshot == nil || *view.Snapshot.Hours != 3 || *view.Snapshot.Tokens != 6_000_000 || *view.Snapshot.ModelEstimate.Hours != 6 {
		t.Fatalf("snapshot lost size or speed: %+v", view.Snapshot)
	}
	if view.Snapshot.RateBasis.Speed != 2 || float64(*view.Snapshot.Tokens) != *view.Snapshot.Hours*float64(view.Snapshot.RateBasis.TokensPerHour)*view.Snapshot.RateBasis.Speed {
		t.Fatalf("frozen rate cannot reproduce estimate: %+v", view.Snapshot)
	}
	code, raw := call(t, &w.admin, "PATCH", "/api/nodes/"+target.ID, `{"fields":{"estimate_hours":5}}`)
	if code != 200 {
		t.Fatalf("update: %d %s", code, raw)
	}
	view = planningOf(t, w.admin, path)[target.Key]
	if *view.Snapshot.Hours != 3 || *view.Snapshot.ModelEstimate.Hours != 6 || *view.Snapshot.Tokens != 6_000_000 {
		t.Fatal("work-start snapshot changed", view.Snapshot)
	}
	// A caller who can read nodes workspace-wide still sees learning only
	// from the project where harness.read is granted.
	hidden := w
	hidden.root = w.node(t, "LHIDDEN-1", "project", "", "open", nil)
	for i := range 5 {
		seedLearningTicket(t, hidden, i+8, int64(4*3600000), false)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
   SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, w.admin.TenantID, w.viewer.ID, w.root.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scoped := httptest.NewRequest("GET", "/api/usage/model-estimates?profile_id="+profile+"&kind=backend&bucket=complex", nil)
	scoped = scoped.WithContext(tenant.WithPrincipal(scoped.Context(), w.viewer))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, scoped)
	if recorder.Code != 200 {
		t.Fatalf("project history %d: %s", recorder.Code, recorder.Body.String())
	}
	var scopedHistory usagedashboard.ModelEstimateHistory
	if err := json.Unmarshal(recorder.Body.Bytes(), &scopedHistory); err != nil {
		t.Fatal(err)
	}
	if scopedHistory.Tickets != 5 {
		t.Fatalf("cross-project samples: %+v", scopedHistory)
	}
	foreign := addPrincipal(t, "learning-foreign")
	cross := scoped.WithContext(tenant.WithPrincipal(scoped.Context(), foreign))
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, cross)
	if recorder.Code != 404 {
		t.Fatalf("cross-tenant profile history %d: %s", recorder.Code, recorder.Body.String())
	}
	// Visibility is applied before selecting each cell's newest-30 window.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		samples, err := usagedashboard.LoadLearningSamples(t.Context(), tx, []usagedashboard.LearningCell{{Family: "openai", Line: "astra", Effort: "xhigh", Kind: "backend", Bucket: "complex"}}, "", func(string) bool { return false })
		if err == nil && len(samples) != 0 {
			t.Fatal("hidden history leaked")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
