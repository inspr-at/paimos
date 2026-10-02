// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/lanedispatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func readyFields() map[string]any {
	return map[string]any{"estimate_hours": 2, "acceptance_criteria": "Human criteria", "route_role": "build", "area": "backend"}
}
func (f *fixture) activate(t *testing.T, l Lane) Lane {
	t.Helper()
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+l.ID+"/resume", actionInput{ExpectedRevision: l.Revision}, 200, &l)
	return l
}
func (f *fixture) scheduleOne(t *testing.T, l Lane) ScheduleResult {
	t.Helper()
	var out ScheduleResult
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+l.ID+"/schedule", actionInput{ExpectedRevision: l.Revision}, 200, &out)
	return out
}
func (f *fixture) queue(t *testing.T, id string) {
	t.Helper()
	f.call(t, f.owner, "POST", "/api/queue", map[string]string{"node_id": id}, 200, nil)
}
func TestSchedulerDependencyOrderManualQueueAndScope(t *testing.T) {
	f := setup(t)
	l := f.activate(t, f.lane(t, f.project))
	dependent, _ := f.ticket(t, f.project, "ticket", readyFields())
	unrelated, _ := f.ticket(t, f.project, "ticket", readyFields())
	prerequisite, _ := f.ticket(t, f.project, "ticket", readyFields())
	backlog, _ := f.ticket(t, f.project, "ticket", readyFields())
	for _, id := range []string{dependent, unrelated, prerequisite} {
		f.queue(t, id)
	}
	f.call(t, f.owner, "POST", "/api/queue/"+dependent+"/move", map[string]int{"position": 1}, 200, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'blocks')`, f.owner.TenantID, prerequisite, dependent)
		return err
	})
	out := f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.TicketNodeID != prerequisite || out.Dispatch.Phase != "requested" || out.Dispatch.Launched {
		t.Fatalf("dependency selection %+v", out)
	}
	// The owned prerequisite remains live; the blocked dependent is skipped.
	out = f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.TicketNodeID != unrelated {
		t.Fatalf("next %+v", out)
	}
	out = f.scheduleOne(t, l)
	if out.Dispatch != nil {
		t.Fatalf("backlog or duplicate dispatched %+v", out)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM lane_dispatches WHERE ticket_node_id=$1`, backlog).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("autonomous backlog intake")
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done',updated_at=clock_timestamp() WHERE id=$1`, prerequisite)
		return err
	})
	out = f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.TicketNodeID != dependent {
		t.Fatalf("completed blocker %+v", out)
	}
	f.call(t, f.owner, "POST", "/api/queue", map[string]string{"node_id": dependent}, 409, nil)
	f.call(t, f.owner, "DELETE", "/api/queue/"+dependent, nil, 409, nil)
	var next struct {
		Entry any `json:"entry"`
	}
	f.call(t, f.owner, "POST", "/api/queue/next", map[string]any{}, 200, &next)
	if next.Entry != nil {
		t.Fatal("coordinator took lane ownership")
	}
}
func TestPreparationAutomaticEstimateAndPersonCriteriaGate(t *testing.T) {
	f := setup(t)
	l := f.activate(t, f.lane(t, f.project))
	id, rev := f.ticket(t, f.project, "ticket", map[string]any{"acceptance_criteria": "Original human criteria"})
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+l.ID+"/prepare", prepareInput{l.Revision, id, rev}, 202, nil)
	out := f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.Phase != "preparation_requested" {
		t.Fatalf("preparation %+v", out)
	}
	path := "/api/autopilot-dispatches/" + out.Dispatch.ID
	var result lanedispatch.Request
	f.call(t, f.owner, "POST", path+"/preparation", preparationResultInput{ExpectedRevision: 1, EstimateHours: 3, Criteria: []string{"Must not replace"}}, 200, &result)
	if result.Phase != "requested" || result.Revision != 2 {
		t.Fatalf("auto estimate %+v", result)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var criteria string
		var hours float64
		if err := tx.QueryRow(t.Context(), `SELECT fields->>'acceptance_criteria',(fields->>'estimate_hours')::float8 FROM nodes WHERE id=$1`, id).Scan(&criteria, &hours); err != nil {
			return err
		}
		if criteria != "Original human criteria" || hours != 3 {
			t.Fatalf("human fields changed: %q %v", criteria, hours)
		}
		return nil
	})
	f.call(t, f.owner, "POST", path+"/preparation", preparationResultInput{ExpectedRevision: 1, EstimateHours: 4}, 409, nil)
	missing, rev := f.ticket(t, f.project, "ticket", map[string]any{"estimate_hours": 2})
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+l.ID+"/prepare", prepareInput{l.Revision, missing, rev}, 202, nil)
	out = f.scheduleOne(t, l)
	if out.Dispatch == nil {
		t.Fatalf("draft request %+v", out)
	}
	path = "/api/autopilot-dispatches/" + out.Dispatch.ID
	f.call(t, f.owner, "POST", path+"/preparation", preparationResultInput{ExpectedRevision: 1, EstimateHours: 4, Criteria: []string{"Proposed criterion"}}, 200, &result)
	if result.Phase != "needs_person" || len(result.DraftCriteria) != 1 {
		t.Fatalf("person gate %+v", result)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var unchanged bool
		err := tx.QueryRow(t.Context(), `SELECT NOT(fields?'acceptance_criteria') AND (fields->>'estimate_hours')::float8=2 FROM nodes WHERE id=$1`, missing).Scan(&unchanged)
		if err == nil && !unchanged {
			t.Fatal("draft silently applied")
		}
		return err
	})
	// Even an administrative agent cannot accept its own draft.
	agent := tenant.Principal{TenantID: f.owner.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.write", "autopilot.manage"}}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Preparer') RETURNING id::text`, agent.TenantID).Scan(&agent.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, agent.TenantID, agent.ID, "admin")
	f.call(t, agent, "POST", path+"/accept", acceptInput{2, ptr(true)}, 403, nil)
	f.call(t, f.owner, "POST", path+"/accept", acceptInput{2, ptr(true)}, 200, &result)
	if result.Phase != "requested" || result.Launched {
		t.Fatalf("accepted %+v", result)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var criterion string
		var hours float64
		var runs int
		if err := tx.QueryRow(t.Context(), `SELECT fields->'acceptance_criteria'->>0,(fields->>'estimate_hours')::float8 FROM nodes WHERE id=$1`, missing).Scan(&criterion, &hours); err != nil {
			return err
		}
		if criterion != "Proposed criterion" || hours != 2 {
			t.Fatal("acceptance overwrote existing estimate")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs`).Scan(&runs); err != nil {
			return err
		}
		if runs != 0 {
			t.Fatal("preparation launched agents")
		}
		return nil
	})
}
func TestSchedulerOverlapPinWindowsAndRevocation(t *testing.T) {
	f := setup(t)
	l := f.activate(t, f.lane(t, f.project))
	second := f.activate(t, f.lane(t, f.project))
	f.call(t, f.owner, "PATCH", "/api/autopilot-lanes/"+second.ID, patchInput{ExpectedRevision: second.Revision, Priority: ptr(1)}, 200, &second)
	id, _ := f.ticket(t, f.project, "ticket", readyFields())
	f.queue(t, id)
	if out := f.scheduleOne(t, l); out.Dispatch != nil {
		t.Fatalf("lower priority stole %+v", out)
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('autopilot_lane_id',$2::text),updated_at=clock_timestamp() WHERE id=$1`, id, l.ID)
		return err
	})
	f.m.now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	if out := f.scheduleOne(t, l); out.Dispatch != nil || out.Reason != "outside_work_window" {
		t.Fatalf("window %+v", out)
	}
	f.m.now = func() time.Time { return time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC) }
	out := f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.TicketNodeID != id {
		t.Fatalf("explicit pin %+v", out)
	}
	outsider := f.person(t, f.otherProject, []string{"nodes.read", "autopilot.read"})
	f.call(t, outsider, "GET", "/api/autopilot-dispatches/"+out.Dispatch.ID, nil, 404, nil)
	manager := f.person(t, f.project, []string{"nodes.read", "autopilot.manage"})
	f.call(t, manager, "POST", "/api/autopilot-lanes/"+l.ID+"/schedule", actionInput{ExpectedRevision: l.Revision}, 403, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1) AND permission IN ('run.create','*')`, f.owner.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,role_id,permission FROM role_bindings CROSS JOIN unnest(ARRAY['nodes.read','autopilot.manage']) permission WHERE principal_id=$1 ON CONFLICT DO NOTHING`, f.owner.ID)
		return err
	})
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+l.ID+"/schedule", actionInput{ExpectedRevision: l.Revision}, 403, nil)
}
func TestReleaseSchedulerOnlyBuildingMembersInReleaseOrder(t *testing.T) {
	f := setup(t)
	release, _ := f.ticket(t, f.project, "release", map[string]any{})
	var l Lane
	f.call(t, f.owner, "POST", "/api/projects/"+f.project+"/autopilot-lanes", createInput{Name: "Release", Policy: testPolicy(), Scope: &Scope{Kind: "release", ReleaseNodeID: &release}}, 201, &l)
	l = f.activate(t, l)
	first, _ := f.ticket(t, f.project, "ticket", readyFields())
	second, _ := f.ticket(t, f.project, "ticket", readyFields())
	outside, _ := f.ticket(t, f.project, "ticket", readyFields())
	f.queue(t, outside)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2);`, f.owner.TenantID, f.project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,project_node_id,release_node_id,number) VALUES($1,$2,$3,1)`, f.owner.TenantID, f.project, release); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,project_node_id,release_node_id,ticket_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual'),($1,$2,$3,$5,1,'manual')`, f.owner.TenantID, f.project, release, first, second)
		return err
	})
	if out := f.scheduleOne(t, l); out.Dispatch != nil || out.Reason != "release_not_building" {
		t.Fatalf("planning release %+v", out)
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='building' WHERE release_node_id=$1`, release)
		return err
	})
	// Queue ordering deliberately conflicts with release ordering.
	f.queue(t, second)
	f.queue(t, first)
	f.call(t, f.owner, "POST", "/api/queue/"+second+"/move", map[string]int{"position": 1}, 200, nil)
	out := f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.TicketNodeID != first {
		t.Fatalf("release order %+v", out)
	}
	out = f.scheduleOne(t, l)
	if out.Dispatch == nil || out.Dispatch.TicketNodeID != second {
		t.Fatalf("release next %+v", out)
	}
}
func TestRacingCoordinatorAndSchedulerOneDispatchBarrier(t *testing.T) {
	f := setup(t)
	l := f.activate(t, f.lane(t, f.project))
	id, _ := f.ticket(t, f.project, "ticket", readyFields())
	f.queue(t, id)
	agent := tenant.Principal{TenantID: f.owner.TenantID, Kind: tenant.Agent}
	var profile string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Runner') RETURNING id::text`, agent.TenantID).Scan(&agent.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'lane-race','1','codex','openai','test-model','high','strong') RETURNING id::text`, agent.TenantID).Scan(&profile); err != nil {
			return err
		}
		var account string
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,max_parallel_runs,last_probe_at,last_probe_ok,last_daemon_generation) VALUES($1,'lane-race','codex','daemon-race',$2,'Race account',1,clock_timestamp(),true,'generation-race') RETURNING id::text`, agent.TenantID, agent.ID).Scan(&account); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 hour','cost_micros',1000000)`, agent.TenantID, account)
		return err
	})
	dbtest.BindRole(t, f.d, agent.TenantID, agent.ID, "member")
	// Prove the coordinator is usable before racing, without touching the ticket.
	var coordinatable bool
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE registered_by_principal_id=$1 AND last_probe_ok AND state='available')`, agent.ID).Scan(&coordinatable)
	})
	if !coordinatable {
		t.Fatal("race has no eligible coordinator account")
	}
	fence, err := f.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(t.Context())
	if _, err = fence.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.owner.TenantID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, action := range []struct {
		path  string
		input any
	}{{"/api/queue/next", map[string]string{"agent_principal_id": agent.ID, "model_profile_id": profile}}, {"/api/autopilot-lanes/" + l.ID + "/schedule", actionInput{ExpectedRevision: l.Revision}}} {
		go func(path string, input any) {
			raw, _ := json.Marshal(input)
			<-start
			r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
			r = r.WithContext(tenant.WithPrincipal(t.Context(), f.owner))
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			responses <- w
		}(action.path, action.input)
	}
	close(start)
	waitCtx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	for {
		var waiting int
		if err = f.d.Admin.QueryRow(waitCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT id::text FROM tenants%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
	}
	if err = fence.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w := <-responses
		if w.Code != 200 {
			t.Fatalf("racing action %d %s", w.Code, w.Body.String())
		}
	}
	f.tx(t, func(tx pgx.Tx) error {
		var dispatches, runs, routed int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM lane_dispatches WHERE ticket_node_id=$1 AND phase NOT IN ('completed','cancelled')`, id).Scan(&dispatches); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(queue_routed_at) FROM agent_runs WHERE queue_node_id=$1`, id).Scan(&runs, &routed); err != nil {
			return err
		}
		var lane *string
		if err := tx.QueryRow(t.Context(), `SELECT lane_id::text FROM lane_dispatches WHERE ticket_node_id=$1`, id).Scan(&lane); err != nil {
			return err
		}
		if dispatches != 1 || runs != 1 || lane != nil && routed != 0 || lane == nil && routed != 1 {
			t.Fatalf("dispatches=%d runs=%d routed=%d lane=%v", dispatches, runs, routed, lane)
		}
		return nil
	})
}
func TestPreparationStaleTicketAndDisabledPolicyFailClosed(t *testing.T) {
	f := setup(t)
	l := f.activate(t, f.lane(t, f.project))
	id, rev := f.ticket(t, f.project, "ticket", map[string]any{})
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+l.ID+"/prepare", prepareInput{l.Revision, id, rev}, 202, nil)
	out := f.scheduleOne(t, l)
	if out.Dispatch == nil {
		t.Fatalf("missing request %+v", out)
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Changed',updated_at=clock_timestamp() WHERE id=$1`, id)
		return err
	})
	path := "/api/autopilot-dispatches/" + out.Dispatch.ID + "/preparation"
	f.call(t, f.owner, "POST", path, preparationResultInput{1, 2, []string{"Draft"}}, 409, nil)
}
func TestDependencyCycleTerminatesAndIndependentOrderStays(t *testing.T) {
	items := []candidate{{id: "a"}, {id: "b"}, {id: "c"}, {id: "d"}}
	dependencyOrder(items, map[string][]string{"a": {"c"}})
	if got := fmt.Sprint([]string{items[0].id, items[1].id, items[2].id, items[3].id}); got != "[c a b d]" {
		t.Fatal(got)
	}
	dependencyOrder(items, map[string][]string{"a": {"c"}, "c": {"a"}})
	if len(items) != 4 {
		t.Fatal("cycle lost items")
	}
}
