// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

type qEntry struct {
	Undo *struct {
		RunID    string `json:"run_id"`
		Revision string `json:"revision"`
	} `json:"undo"`
	NodeID string `json:"node_id"`
	State  string
	Queued *workqueue.Queued
	Run    agentruns.Run
}
type qPage struct {
	Items    []qEntry
	Count    int
	Manual   bool `json:"manual_order"`
	Capacity struct {
		Hours    float64  `json:"queued_hours"`
		Parallel int      `json:"parallel_runs"`
		Work     *float64 `json:"work_hours"`
	}
}

func (f *fixture) ticket(t *testing.T, state, priority string, fields map[string]any) string {
	t.Helper()
	if fields == nil {
		fields = map[string]any{"estimate_hours": 2, "acceptance_criteria": "- [ ] tests pass"}
	}
	fields["priority"] = priority
	raw, _ := json.Marshal(fields)
	var id string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields)
 SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Work',$2,$3 FROM node_kinds k WHERE k.slug='ticket' RETURNING id::text`, f.person.TenantID, state, raw).Scan(&id)
	})
	return id
}
func (f *fixture) addQueue(t *testing.T, id string, target map[string]any) qEntry {
	t.Helper()
	if target == nil {
		target = map[string]any{}
	}
	target["node_id"] = id
	var e qEntry
	f.call(t, f.person, "POST", "/api/queue", target, 200, &e)
	return e
}
func (f *fixture) queuePage(t *testing.T) qPage {
	t.Helper()
	var out qPage
	f.call(t, f.person, "GET", "/api/queue", nil, 200, &out)
	return out
}
func keys(p qPage) []string {
	out := []string{}
	for _, e := range p.Items {
		out = append(out, e.NodeID)
	}
	return out
}
func (f *fixture) queueAccount(t *testing.T, allowance int64) string {
	t.Helper()
	id := uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,max_parallel_runs,last_probe_at,last_probe_ok,last_daemon_generation) VALUES($1,$2::uuid,$2::text,'codex','daemon-test',$3,'Queue test',1,clock_timestamp(),true,'generation-1')`, f.agent.TenantID, id, f.agent.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 hour','cost_micros',$3)`, f.agent.TenantID, id, allowance)
		return err
	})
	roundTheClockAccount(t, f, id)
	return id
}
func TestTicketQueueOrderResetReadinessAndPayload(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	low := f.ticket(t, "new", "low", nil)
	high := f.ticket(t, "backlog", "high", nil)
	second := f.ticket(t, "open", "high", nil)
	e := f.addQueue(t, low, nil)
	if e.State != "open" || e.Run.QueueNodeID == nil || e.Queued.By.ID != f.person.ID {
		t.Fatalf("queue entry %+v", e)
	}
	f.addQueue(t, high, nil)
	f.addQueue(t, second, nil)
	if got := keys(f.queuePage(t)); !slices.Equal(got, []string{high, second, low}) {
		t.Fatalf("priority FIFO %v", got)
	}
	// Filtering to one ID must preserve the full visible queue position and head.
	var node struct{ Queued *workqueue.Queued }
	f.call(t, f.person, "GET", "/api/nodes/"+low, nil, 200, &node)
	if node.Queued == nil || node.Queued.Position != 3 || node.Queued.Waiting {
		t.Fatalf("node projection %+v", node.Queued)
	}
	var page qPage
	f.call(t, f.person, "POST", "/api/queue/"+low+"/move", map[string]int{"position": 1}, 200, &page)
	if !page.Manual || !slices.Equal(keys(page), []string{low, high, second}) {
		t.Fatalf("manual order %+v", page)
	}
	late := f.ticket(t, "open", "urgent", nil)
	f.addQueue(t, late, nil)
	if got := keys(f.queuePage(t)); !slices.Equal(got, []string{low, high, second, late}) {
		t.Fatalf("manual arrival %v", got)
	}
	f.call(t, f.person, "POST", "/api/queue/reset", map[string]any{}, 200, &page)
	if page.Manual || !slices.Equal(keys(page), []string{late, high, second, low}) {
		t.Fatalf("reset %+v", page)
	}
	f.call(t, f.person, "DELETE", "/api/queue/"+low, nil, 200, nil)
	f.call(t, f.person, "DELETE", "/api/queue/"+low, nil, 200, nil)
	f.call(t, f.person, "GET", "/api/nodes/"+low, nil, 200, &struct{ State string }{})
	missing := f.ticket(t, "open", "medium", map[string]any{})
	var ready workqueue.Readiness
	f.call(t, f.person, "GET", "/api/queue/"+missing+"/readiness", nil, 200, &ready)
	if ready.Ready || !slices.Equal(ready.Missing, []string{"estimate", "criteria"}) || ready.SuggestedEstimateHours <= 0 {
		t.Fatalf("readiness %+v", ready)
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]string{"node_id": missing}, 422, nil)
	f.call(t, f.person, "POST", "/api/queue/"+missing+"/estimate", map[string]float64{"estimate_hours": ready.SuggestedEstimateHours}, 200, &ready)
	if !slices.Equal(ready.Missing, []string{"criteria"}) {
		t.Fatalf("estimate fix %+v", ready)
	}
	f.call(t, f.person, "POST", "/api/queue/"+missing+"/estimate", map[string]float64{"estimate_hours": 201}, 400, nil)
}
func TestTicketQueueBlockedTargetCapacityAndPickup(t *testing.T) {
	for _, autopilot := range []bool{true, false} {
		t.Run(fmt.Sprintf("autopilot=%t", autopilot), func(t *testing.T) {
			f := setup(t)
			f.queueAccount(t, 1000000)
			if !autopilot {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules) VALUES($1,false,'{"new":{"enabled":true,"days":7},"backlog":{"enabled":true,"days":90},"blocked":{"enabled":true,"days":14},"progress":{"enabled":true,"days":3},"done":{"enabled":true,"days":7},"accept":{"enabled":true,"days":30},"publish":{"enabled":true,"days":0}}')`, f.person.TenantID)
					return err
				})
			}
			blocked := f.ticket(t, "blocked", "high", map[string]any{"estimate_hours": 2, "acceptance_criteria": "test"})
			f.call(t, f.person, "POST", "/api/queue", map[string]string{"node_id": blocked}, 422, nil)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"blocker":"QUE-1"}'::jsonb WHERE id=$1`, blocked)
				return err
			})
			b := f.addQueue(t, blocked, nil)
			if !b.Queued.Waiting || b.State != "blocked" {
				t.Fatalf("blocked entry %+v", b)
			}
			ready := f.ticket(t, "open", "low", nil)
			e := f.addQueue(t, ready, nil)
			var picked struct{ Entry *qEntry }
			target := map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}
			f.call(t, f.person, "POST", "/api/queue/next", target, 200, &picked)
			if picked.Entry == nil || picked.Entry.NodeID != ready || picked.Entry.Run.QueueRoutedAt == nil {
				t.Fatalf("next %+v", picked)
			}
			f.call(t, f.person, "POST", "/api/queue/next", target, 200, &picked)
			if picked.Entry == nil || picked.Entry.Run.ID != e.Run.ID {
				t.Fatal("next was not idempotent")
			}
			// Actual claims still need exactly the daemon's reservation set.
			ids := f.reserve(t, picked.Entry.Run)
			f.call(t, f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", claimBody(ids), 200, nil)
			var state string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, ready).Scan(&state)
			})
			if state != "in_progress" {
				t.Fatalf("pickup state %s", state)
			}
			if got := keys(f.queuePage(t)); !slices.Equal(got, []string{blocked}) {
				t.Fatalf("pickup did not leave queue: %v", got)
			}
			f.call(t, f.person, "DELETE", "/api/queue/"+ready, nil, 409, nil)
		})
	}
}
func TestTicketQueueTargetedStartNowAndAllowance(t *testing.T) {
	f := setup(t)
	account := f.queueAccount(t, 1)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET used=allowance WHERE account_id=$1`, account)
		return err
	})
	shared := f.ticket(t, "open", "urgent", nil)
	f.addQueue(t, shared, nil)
	id := f.ticket(t, "open", "high", nil)
	target := map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account}
	e := f.addQueue(t, id, target)
	if !e.Queued.Targeted || e.Queued.Position != 1 || !e.Queued.Waiting {
		t.Fatalf("targeted %+v", e.Queued)
	}
	f.call(t, f.person, "POST", "/api/queue/"+id+"/move", map[string]int{"position": 1}, 409, nil)
	var picked struct{ Entry *qEntry }
	f.call(t, f.person, "POST", "/api/queue/next", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account}, 200, &picked)
	if picked.Entry != nil {
		t.Fatal("exhausted allowance selected work")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET allowance=1000000 WHERE account_id=$1`, account)
		return err
	})
	newer := f.ticket(t, "open", "low", nil)
	next := f.addQueue(t, newer, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account})
	if next.Queued.Position != 1 {
		t.Fatal("Start now did not become first for agent")
	}
	var poll []agentruns.Run
	f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &poll)
	if len(poll) != 2 || poll[0].ID != next.Run.ID || poll[1].ID != e.Run.ID {
		t.Fatalf("daemon poll lost Start now precedence: %+v", poll)
	}
	f.call(t, f.person, "POST", "/api/queue/next", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 200, &picked)
	if picked.Entry == nil || picked.Entry.NodeID != newer {
		t.Fatalf("targeted work did not precede shared %+v", picked)
	}
	f.call(t, f.person, "DELETE", "/api/queue/"+newer, nil, 200, nil)
}
func TestTicketQueuePermissionsIsolationAuditAndConcurrentAdd(t *testing.T) {
	f := setup(t)
	id := f.ticket(t, "open", "high", nil)
	worker := f.agent
	worker.Scopes = []string{"nodes.read", "run.create"}
	f.call(t, worker, "POST", "/api/queue", map[string]string{"node_id": id}, 403, nil)
	coordinator := f.other
	coordinator.Scopes = authz.CoordinatorKeyScopes
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'queue_coordinator','Queue coordinator') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		for _, permission := range authz.CoordinatorBaseScopes {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, role, permission); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'`, coordinator.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.person.TenantID, coordinator.ID, role)
		return err
	})
	var e qEntry
	f.call(t, coordinator, "POST", "/api/queue", map[string]string{"node_id": id}, 200, &e)
	if e.Queued.By.ID != coordinator.ID || e.Queued.By.Kind != "agent" {
		t.Fatal("coordinator attribution missing")
	}
	dbtest.BindRole(t, f.d, f.foreign.TenantID, f.foreign.ID, "admin")
	f.call(t, f.foreign, "GET", "/api/queue", nil, 200, &qPage{})
	f.call(t, f.foreign, "DELETE", "/api/queue/"+id, nil, 404, nil)
	f.call(t, f.foreign, "POST", "/api/queue/"+id+"/move", map[string]int{"position": 1}, 404, nil)
	// Creator's rights intersect a coordinator's live grants.
	viewer := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(id,tenant_id,kind,name) VALUES($1,$2,'person','Viewer')`, viewer.ID, viewer.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `WITH reader AS(INSERT INTO roles(tenant_id,key,name) VALUES($1,'queue_reader','Queue reader') RETURNING tenant_id,id)
 INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,id,'nodes.read' FROM reader`, viewer.TenantID)
		return err
	})
	dbtest.BindRole(t, f.d, viewer.TenantID, viewer.ID, "queue_reader")
	var visible map[string]any
	f.call(t, viewer, "GET", "/api/queue", nil, 200, &visible)
	rows := visible["items"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["run"] != nil || rows[0].(map[string]any)["queued"] == nil {
		t.Fatalf("ticket viewer run disclosure: %+v", visible)
	}
	f.call(t, viewer, "DELETE", "/api/queue/"+id, nil, 403, nil)
	capped := coordinator
	capped.KeyCreatorID = viewer.ID
	f.call(t, capped, "POST", "/api/queue/"+id+"/move", map[string]int{"position": 1}, 403, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, coordinator.ID)
		return err
	})
	f.call(t, coordinator, "DELETE", "/api/queue/"+id, nil, 403, nil)
	other := f.ticket(t, "open", "high", nil)
	body := `{"node_id":"` + other + `"}`
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for range 6 {
		wg.Go(func() { codes <- f.request(f.person, http.MethodPost, "/api/queue", body, "").Code })
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent add: %d", code)
		}
	}
	var runs, events int
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_runs WHERE queue_node_id=$1),(SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.added')`, other).Scan(&runs, &events)
	})
	if runs != 1 || events != 1 {
		t.Fatalf("duplicate queue/audit: %d %d", runs, events)
	}
}

func TestTicketQueueAutomaticSecurityRoutingAndProjectVisibility(t *testing.T) {
	f := setup(t)
	f.queueAccount(t, 1000000)
	security := f.ticket(t, "open", "high", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Check tenant permissions' WHERE id=$1`, security); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build-hard',1,$2)
 ON CONFLICT(tenant_id,role,priority) DO UPDATE SET profile_id=excluded.profile_id`, f.person.TenantID, f.profile)
		return err
	})
	e := f.addQueue(t, security, nil)
	if !e.Queued.SecurityReview || e.Queued.ExpectedAgentID != nil {
		t.Fatalf("unrouted security work: %+v", e.Queued)
	}
	var picked struct{ Entry *qEntry }
	f.call(t, f.person, "POST", "/api/queue/next", map[string]any{}, 200, &picked)
	if picked.Entry == nil || picked.Entry.Run.AgentID != f.agent.ID || picked.Entry.Run.ProfileID == nil || *picked.Entry.Run.ProfileID != f.profile {
		t.Fatalf("automatic build-hard selection: %+v", picked.Entry)
	}
	var securityFlags bool
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT fields->>'security_review_required'='true' AND fields->>'needs_review'='true' AND fields->>'review_route'='review-gate' FROM nodes WHERE id=$1`, security).Scan(&securityFlags)
	})
	if !securityFlags {
		t.Fatal("security review flags missing")
	}

	guest := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	var project string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(id,tenant_id,kind,name) VALUES($1,$2,'person','Project member')`, guest.ID, guest.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Visible project','open' FROM node_kinds WHERE slug='project' RETURNING id::text`, guest.TenantID).Scan(&project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, guest.TenantID, guest.ID, project)
		return err
	})
	visible := f.ticket(t, "open", "low", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, visible, project)
		return err
	})
	f.call(t, guest, "POST", "/api/queue", map[string]string{"node_id": visible}, 200, &e)
	var page qPage
	f.call(t, guest, "GET", "/api/queue", nil, 200, &page)
	if page.Count != 1 || page.Items[0].NodeID != visible || page.Items[0].Queued.Position != 1 {
		t.Fatalf("hidden project affected count or position: %+v", page)
	}
	f.call(t, guest, "POST", "/api/queue/"+security+"/move", map[string]int{"position": 1}, 404, nil)
	f.call(t, guest, "DELETE", "/api/queue/"+security, nil, 404, nil)
	f.call(t, guest, "POST", "/api/queue/"+visible+"/move", map[string]int{"position": 1}, 200, &page)
	if got := keys(f.queuePage(t)); !slices.Equal(got, []string{security, visible}) {
		t.Fatalf("project move changed global order: %v", got)
	}
	hidden := f.ticket(t, "open", "urgent", nil)
	f.addQueue(t, hidden, nil)
	second := f.ticket(t, "open", "low", nil)
	arrival := f.ticket(t, "open", "urgent", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=ANY($1::uuid[])`, []string{second, arrival}, project)
		return err
	})
	f.call(t, guest, "POST", "/api/queue", map[string]string{"node_id": second}, 200, nil)
	f.call(t, guest, "POST", "/api/queue/"+second+"/move", map[string]int{"position": 1}, 200, &page)
	if page.Count != 2 || !slices.Equal(keys(page), []string{second, visible}) {
		t.Fatalf("project move response leaked hidden work: %+v", page)
	}
	if got := keys(f.queuePage(t)); !slices.Equal(got, []string{security, second, hidden, visible}) {
		t.Fatalf("move failed to preserve hidden slots: %v", got)
	}
	f.call(t, guest, "POST", "/api/queue", map[string]string{"node_id": arrival}, 200, nil)
	if got := keys(f.queuePage(t)); !slices.Equal(got, []string{security, second, hidden, visible, arrival}) {
		t.Fatalf("project append ignored hidden ranks: %v", got)
	}
	f.call(t, guest, "POST", "/api/queue/reset", map[string]any{}, 200, &page)
	global := f.queuePage(t)
	if global.Manual || !slices.Equal(keys(global), []string{hidden, arrival, security, visible, second}) {
		t.Fatalf("project reset left hidden ranks: %+v", global)
	}
	f.call(t, f.person, "POST", "/api/queue/"+security+"/move", map[string]int{"position": 1}, 200, nil)
	for _, id := range []string{visible, second, arrival} {
		f.call(t, guest, "DELETE", "/api/queue/"+id, nil, 200, nil)
	}
	f.call(t, guest, "POST", "/api/queue/reset", map[string]any{}, 200, &page)
	global = f.queuePage(t)
	if page.Count != 0 || !global.Manual || !slices.Equal(keys(global), []string{security, hidden}) {
		t.Fatalf("empty visible reset modified hidden work: %+v", global)
	}
}

// Regression: this positive recovery and every inverse fence fail on 9d81acc6.
func TestStaleQueueRecoveryAndUndo(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	id := f.ticket(t, "in_progress", "high", map[string]any{"estimate_hours": 2, "acceptance_criteria": "Works", "security_review_required": true, "needs_review": false, "review_route": "manual", "custom": "keep"})
	var node struct {
		State      string
		Fields     json.RawMessage
		QueueStale bool `json:"queue_stale"`
	}
	f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 200, &node)
	if !node.QueueStale {
		t.Fatal("idle progress was not projected as stale")
	}
	original := append(json.RawMessage(nil), node.Fields...)
	e := f.addQueue(t, id, nil)
	if e.State != "open" || e.Queued == nil || e.Undo == nil || e.Undo.RunID != e.Queued.RunID || e.Undo.Revision == "" {
		t.Fatalf("recovery entry %+v", e)
	}
	f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 200, &node)
	if node.State != "open" || node.QueueStale {
		t.Fatalf("queued node %+v", node)
	}
	var result struct{ Removed bool }
	f.call(t, f.person, "POST", "/api/queue/"+id+"/undo", e.Undo, 200, &result)
	if !result.Removed || len(f.queuePage(t).Items) != 0 {
		t.Fatal("Undo failed to remove the queued run")
	}
	f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 200, &node)
	if node.State != "in_progress" || !node.QueueStale {
		t.Fatalf("Undo did not restore stale progress: %+v", node)
	}
	var status string
	var restored bool
	var additions, removals, undos, created int
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT status FROM agent_runs WHERE id=$2),
 (SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.added'),
 (SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.removed'),
 (SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.add_undone'),
 (SELECT count(*) FROM events WHERE node_id=$3 AND type='node.created'),
 (SELECT fields=$4::jsonb FROM nodes WHERE id=$1)`, id, e.Undo.RunID, e.Run.OrderID, original).Scan(&status, &additions, &removals, &undos, &created, &restored)
	})
	if !restored {
		t.Fatal("Undo did not restore the original fields")
	}
	if status != "cancelled" || additions != 1 || removals != 1 || undos != 1 || created != 1 {
		t.Fatalf("run/audit = %s %d %d %d %d", status, additions, removals, undos, created)
	}
	f.call(t, f.person, "POST", "/api/queue/"+id+"/undo", e.Undo, 409, nil)
}

func TestStaleQueueEligibility(t *testing.T) {
	f := setup(t)
	for _, state := range []string{"in_progress", "In progress", "in-progress", "progress", "active"} {
		id := f.ticket(t, state, "high", nil)
		var ready struct{ Queueable, Ready, Stale bool }
		f.call(t, f.person, "GET", "/api/queue/"+id+"/readiness", nil, 200, &ready)
		if !ready.Queueable || !ready.Ready || !ready.Stale {
			t.Fatalf("idle %s readiness %+v", state, ready)
		}
		f.addQueue(t, id, nil)
	}
	for _, assignment := range []map[string]any{
		{"assignee": f.person.ID}, {"assignee": map[string]any{"id": f.person.ID}},
		{"assignee_id": f.person.ID}, {"classic": map[string]any{"assignee_id": "42"}},
	} {
		assignment["estimate_hours"] = 2
		assignment["acceptance_criteria"] = "Tests pass"
		id := f.ticket(t, "in_progress", "high", assignment)
		var refusal struct {
			Code      string
			Readiness struct {
				Missing   []string
				Queueable bool
			}
		}
		f.call(t, f.person, "POST", "/api/queue", map[string]string{"node_id": id}, 422, &refusal)
		if refusal.Code != "queue_not_ready" || refusal.Readiness.Queueable || !slices.Contains(refusal.Readiness.Missing, "status") {
			t.Fatalf("wrong rejection %+v", refusal)
		}
	}
	// A queued work order attached outside the ticket queue still counts as live.
	id := f.ticket(t, "in_progress", "high", nil)
	o := f.order(t, nil)
	f.run(t, o)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, o.NodeID, id)
		return err
	})
	var ready struct{ Queueable, Stale bool }
	f.call(t, f.person, "GET", "/api/queue/"+id+"/readiness", nil, 200, &ready)
	if ready.Queueable || ready.Stale {
		t.Fatal("active order counted as idle")
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]string{"node_id": id}, 422, nil)
}

func TestStaleQueueUndoFences(t *testing.T) {
	for _, reason := range []string{"revision", "assignment", "started", "replaced", "permission", "actor", "foreign"} {
		t.Run(reason, func(t *testing.T) {
			f := setup(t)
			id := f.ticket(t, "in_progress", "high", nil)
			e := f.addQueue(t, id, nil)
			if e.Undo == nil {
				t.Fatal("missing Undo token")
			}
			p, status := f.person, 409
			switch reason {
			case "revision", "assignment":
				f.tx(t, f.person, func(tx pgx.Tx) error {
					fields := `{}`
					if reason == "assignment" {
						fields = `{"assignee":"` + f.person.ID + `"}`
					}
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Changed',fields=fields||$2::jsonb,updated_at=updated_at+interval '1 microsecond' WHERE id=$1`, id, fields)
					return err
				})
			case "started":
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='starting' WHERE id=$1`, e.Undo.RunID)
					return err
				})
			case "replaced":
				f.call(t, f.person, "DELETE", "/api/queue/"+id, nil, 200, nil)
				f.addQueue(t, id, nil)
			case "permission":
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES(current_setting('aeon.tenant_id')::uuid,'stale_reader','Reader'); INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,id,'nodes.read' FROM roles WHERE key='stale_reader'`)
					return err
				})
				dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "stale_reader")
				status = 403
			case "actor":
				p.ID = uuid()
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Other actor')`, p.TenantID, p.ID)
					return err
				})
				dbtest.BindRole(t, f.d, p.TenantID, p.ID, "admin")
			case "foreign":
				p = f.foreign
				dbtest.BindRole(t, f.d, p.TenantID, p.ID, "admin")
				status = 404
			}
			f.call(t, p, "POST", "/api/queue/"+id+"/undo", e.Undo, status, nil)
			var state string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, id).Scan(&state)
			})
			if state != "open" {
				t.Fatalf("refused Undo changed state to %s", state)
			}
		})
	}
}

func TestStaleQueueSessionEligibility(t *testing.T) {
	f := setup(t)
	var project string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project','open' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project)
	})
	for _, stopped := range []bool{false, true} {
		id := f.ticket(t, "in_progress", "high", nil)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,stopped_at,stop_reason)
 VALUES($1,$2,$3,$4,'codex','local','unmanaged','worker','ship',$6,decode(repeat('cd',32),'hex'),CASE WHEN $5 THEN 'stopped' ELSE 'working' END,CASE WHEN $5 THEN now() END,CASE WHEN $5 THEN 'exited' END)`, f.person.TenantID, project, f.agent.ID, id, stopped, []byte(id[:32]))
			return err
		})
		var ready struct{ Queueable, Stale bool }
		f.call(t, f.person, "GET", "/api/queue/"+id+"/readiness", nil, 200, &ready)
		if ready.Queueable != stopped || ready.Stale != stopped {
			t.Fatalf("stopped=%t readiness %+v", stopped, ready)
		}
		if stopped {
			f.addQueue(t, id, nil)
		} else {
			f.call(t, f.person, "POST", "/api/queue", map[string]string{"node_id": id}, 422, nil)
		}
	}
}
