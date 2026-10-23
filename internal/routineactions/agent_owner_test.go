// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks: unattended spend, duplicate outputs, actor substitution, cross-tenant
// writes and stale/revoked person grants. One database-derived clock drives
// both saved schedule dates and the simulated scheduler; no calendar bomb.
func TestAgentOwnedRoutineResearchBudgetAndAuthority(t *testing.T) {
	f := agentOwnedSetup(t)
	f.p.BrowserSession = true
	f.tx(func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&f.now) })
	permissions := []string{"nodes.read", "nodes.write", Permission, "knowledge.write", "comments.write", "inbox.read"}
	agent := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Agent, Name: "Research agent", Scopes: permissions}
	var role string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent',$2,true) RETURNING id::text`, agent.TenantID, agent.Name).Scan(&agent.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'routine_research','Routine researcher') RETURNING id::text`, agent.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, agent.TenantID, role, permissions); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, agent.TenantID, agent.ID, role, f.project)
		return err
	})
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO inbox_message_targets(tenant_id,project_id,principal_id,address,adapter,target_kind,maximum_level,role,version,sealed_target,has_secret) VALUES($1,$2,$3,'grok_bot:research','grok_bot_routine','webhook','simple','primary',1,decode('00','hex'),false)`, agent.TenantID, f.project, agent.ID)
		return err
	})
	scoped := func() Input {
		in := f.input()
		in.QueueEach = false
		in.OverlapPolicy = "create"
		in.Template.Title = "Weekly research {{occurrence}}"
		in.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: f.p.ID}
		return in
	}
	grant := func(item Recurrence, destination AgentDestination) Recurrence {
		t.Helper()
		f.call(f.p, "PUT", "/api/recurrences/"+item.ID+"/agent-owner", map[string]any{"expected_revision": item.Revision, "owner_principal_id": agent.ID, "destination": destination}, 200)
		return f.get(item.ID)
	}
	dispatch := func() {
		t.Helper()
		if err := f.m.DispatchAgentRuns(t.Context(), f.p.TenantID); err != nil {
			t.Fatal(err)
		}
	}
	readRun := func(id string) (string, string, int) {
		t.Helper()
		var state, reason string
		var count int
		f.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT r.state,coalesce(o.result->>'reason',''),(SELECT count(*) FROM inbox_messages WHERE idempotency_key='routine-run/'||r.id::text) FROM routine_runs r JOIN routine_effect_outbox o ON o.run_id=r.id AND o.effect_key='agent-dispatch' WHERE r.id=$1`, id).Scan(&state, &reason, &count)
		})
		return state, reason, count
	}
	complete := func(item Recurrence, run string, p tenant.Principal, in AgentResultInput, status int) AgentResult {
		t.Helper()
		var out AgentResult
		raw := f.call(p, "POST", "/api/recurrences/"+item.ID+"/runs/"+run+"/agent-result", in, status)
		if status == 200 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	// Legacy person-owned schedules retain ordinary ticket-only behavior.
	personOwned := f.create(f.input())
	if raw := f.call(f.p, "GET", "/api/recurrences/"+personOwned.ID+"/agent-owner", nil, 200); strings.TrimSpace(string(raw)) != "null" {
		t.Fatalf("legacy ownership changed: %s", raw)
	}
	legacy := f.manual(personOwned.ID, "legacy")
	if legacy.NodeID == nil || legacy.Run != nil {
		t.Fatalf("legacy occurrence changed: %+v", legacy)
	}
	research := grant(f.create(scoped()), AgentDestination{Kind: "knowledge"})
	if !research.Paused {
		t.Fatal("grant started the schedule")
	}
	f.call(f.p, "POST", "/api/recurrences/"+research.ID+"/resume", map[string]int64{"expected_revision": research.Revision}, 200)
	research = f.get(research.ID)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrences SET next_at=$2 WHERE id=$1`, research.ID, f.now.AddDate(0, 0, -7))
		return err
	})
	f.run()
	receipts := f.receipts(research.ID)
	if len(receipts) != 1 || receipts[0].NodeID == nil {
		t.Fatalf("weekly occurrence missing: %+v", receipts)
	}
	var run string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM routine_runs WHERE recurrence_id=$1`, research.ID).Scan(&run)
	})
	// Missing policy is closed, and a zero remaining budget creates no inbox work.
	dispatch()
	if state, reason, count := readRun(run); state != "held" || reason != "budget_policy_unavailable" || count != 0 {
		t.Fatalf("missing policy: %s %s %d", state, reason, count)
	}
	ready := func() {
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE routine_effect_outbox SET reconcile_after=clock_timestamp() WHERE run_id=$1 AND effect_key='agent-dispatch'`, run)
			return err
		})
	}
	allowed := false
	checks := 0
	f.m.WithAgentBudget(func(_ context.Context, tx pgx.Tx, in AgentBudgetRequest) (AgentBudgetDecision, error) {
		checks++
		var observed time.Time
		if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&observed); err != nil {
			return AgentBudgetDecision{}, err
		}
		if in.TenantID != agent.TenantID || in.OwnerPrincipalID != agent.ID || in.RunID != run || in.WeekStart.Weekday() != time.Monday || in.WeekStart.Hour() != 0 || in.WeekStart.Location() != time.UTC || in.WeekEnd.Sub(in.WeekStart) != 7*24*time.Hour || observed.Before(in.WeekStart) || !observed.Before(in.WeekEnd) {
			return AgentBudgetDecision{}, fmt.Errorf("wrong owner/week binding: %+v", in)
		}
		return AgentBudgetDecision{Allowed: allowed}, nil
	})
	ready()
	dispatch()
	if state, reason, count := readRun(run); state != "held" || reason != "owner_weekly_budget_exhausted" || count != 0 || checks != 1 {
		t.Fatalf("budget exhausted: %s %s %d checks=%d", state, reason, count, checks)
	}
	complete(research, run, agent, AgentResultInput{Status: "succeeded", Title: "Premature result", Body: "not admitted"}, 409)
	allowed = true
	ready()
	dispatch()
	dispatch()
	if state, reason, count := readRun(run); state != "created" || reason != "queued_to_owner" || count != 1 || checks != 2 {
		t.Fatalf("dispatch replay: %s %s %d checks=%d", state, reason, count, checks)
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_compat_messages c JOIN inbox_message_deliveries d ON d.message_id=c.id JOIN inbox_message_targets target ON target.id=d.target_id WHERE c.recipient_principal_id=$1 AND c.body LIKE $2 AND d.state='pending' AND target.adapter='grok_bot_routine'`, agent.ID, "Routine run "+run+"%").Scan(&count)
		if err == nil && count != 1 {
			t.Fatalf("hosted adapter dispatch count=%d", count)
		}
		return err
	})
	// The run is dispatched to its owner; another agent, person or narrow key
	// cannot report the outcome. Foreign tenant IDs are invisible under real RLS.
	narrow := agent
	narrow.Scopes = []string{Permission, "nodes.read"}
	complete(research, run, narrow, AgentResultInput{Status: "succeeded", Title: "No scope", Body: "denied"}, 403)
	complete(research, run, f.p, AgentResultInput{Status: "succeeded", Title: "Wrong actor", Body: "denied"}, 403)
	other := agent
	other.ID = f.p.ID
	complete(research, run, other, AgentResultInput{Status: "succeeded", Title: "Wrong owner", Body: "denied"}, 403)
	foreign := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000001", Kind: tenant.Person, Name: "Foreign", BrowserSession: true}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign-routines','Foreign routines')`, foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign') RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
		t.Fatal(err)
	}
	f.call(foreign, "GET", "/api/recurrences/"+research.ID+"/agent-owner", nil, 404)
	foreignAgent := agent
	foreignAgent.TenantID = foreign.TenantID
	foreignAgent.ID = foreign.ID
	complete(research, run, foreignAgent, AgentResultInput{Status: "succeeded", Title: "Foreign", Body: "denied"}, 404)
	// Person-only ownership cannot be granted by a full-authority agent key.
	f.call(agent, "PUT", "/api/recurrences/"+research.ID+"/agent-owner", map[string]any{"expected_revision": research.Revision, "owner_principal_id": agent.ID, "destination": AgentDestination{Kind: "knowledge"}}, 403)
	apiPerson := f.p
	apiPerson.BrowserSession = false
	f.call(apiPerson, "PUT", "/api/recurrences/"+research.ID+"/agent-owner", map[string]any{"expected_revision": research.Revision, "owner_principal_id": agent.ID, "destination": AgentDestination{Kind: "knowledge"}}, 403)
	// One successful result writes ordinary proposed Knowledge under the agent.
	cost := int64(12500)
	result := AgentResultInput{Status: "succeeded", Title: "Weekly research findings", Body: "Verified research findings", CostMicroUSD: &cost}
	got := complete(research, run, agent, result, 200)
	replay := complete(research, run, agent, result, 200)
	if got.NodeID == "" || got != replay && digestJSON(got) != digestJSON(replay) {
		t.Fatalf("result replay changed: %+v %+v", got, replay)
	}
	result.Body = "Conflicting retry"
	complete(research, run, agent, result, 409)
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id JOIN events e ON e.node_id=n.id AND e.type='knowledge.created' WHERE n.id=$1 AND n.body='Verified research findings' AND k.slug='memory' AND e.actor_principal_id=$2`, got.NodeID, agent.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("knowledge output/attribution count=%d", count)
		}
		return nil
	})
	raw := f.call(f.p, "GET", "/api/recurrences/"+research.ID+"/runs/"+run+"/agent-result", nil, 200)
	if !strings.Contains(string(raw), `"cost_microusd":12500`) {
		t.Fatalf("reported cost missing: %s", raw)
	}
	history := f.call(f.p, "GET", "/api/recurrences/"+research.ID+"/runs", nil, 200)
	if !strings.Contains(string(history), `"state":"done"`) {
		t.Fatalf("result history: %s", history)
	}
	// A later definition edit invalidates the person grant, including a result
	// that had already been dispatched. Restoring role authority is separate.
	f.m.WithAgentBudget(func(context.Context, pgx.Tx, AgentBudgetRequest) (AgentBudgetDecision, error) {
		return AgentBudgetDecision{Allowed: true}, nil
	})
	editRoutine := grant(f.create(scoped()), AgentDestination{Kind: "new_ticket"})
	editRun := f.manual(editRoutine.ID, "edit")
	if editRun.Run == nil {
		t.Fatal("missing agent run")
	}
	dispatch()
	changed := editRoutine.Input
	changed.Template.Description = "Changed after dispatch"
	f.call(f.p, "PUT", "/api/recurrences/"+editRoutine.ID, struct {
		Input
		ExpectedRevision int64 `json:"expected_revision"`
	}{changed, editRoutine.Revision}, 200)
	complete(editRoutine, editRun.Run.ID, agent, AgentResultInput{Status: "succeeded", Title: "Stale output", Body: "must not land"}, 409)
	// Ordinary ticket/comment writers are reused, with output receipts and
	// unknown cost preserved; the hosted target uses the existing Grok outbox.
	for _, kind := range []string{"new_ticket", "ticket_comment"} {
		destination := AgentDestination{Kind: kind}
		if kind == "ticket_comment" {
			destination.TicketID = f.parent
		}
		item := grant(f.create(scoped()), destination)
		occurrence := f.manual(item.ID, kind)
		dispatch()
		output := complete(item, occurrence.Run.ID, agent, AgentResultInput{Status: "succeeded", Title: "Routine ticket", Body: "Routine result body"}, 200)
		if output.NodeID == "" || output.CostMicroUSD != nil {
			t.Fatalf("unknown cost fabricated: %+v", output)
		}
		if kind == "ticket_comment" {
			f.tx(func(tx pgx.Tx) error {
				var count int
				err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='comment.created' AND actor_principal_id=$2 AND after->>'body_markdown'='Routine result body'`, f.parent, agent.ID).Scan(&count)
				if err == nil && count != 1 {
					t.Fatalf("comment count=%d", count)
				}
				return err
			})
		}
	}
	// A failed run retains honest history and creates no successful output.
	failedRoutine := grant(f.create(scoped()), AgentDestination{Kind: "knowledge"})
	failedRun := f.manual(failedRoutine.ID, "failed")
	dispatch()
	failed := complete(failedRoutine, failedRun.Run.ID, agent, AgentResultInput{Status: "failed", Body: "Source unavailable"}, 200)
	if failed.Status != "failed" || failed.NodeID != "" {
		t.Fatalf("failure reported success: %+v", failed)
	}
	// Revoking the live agent role prevents final writes despite dispatch.
	revokedRoutine := grant(f.create(scoped()), AgentDestination{Kind: "knowledge"})
	revokedRun := f.manual(revokedRoutine.ID, "revoke")
	dispatch()
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id=$1 AND permission='knowledge.write'`, role)
		return err
	})
	complete(revokedRoutine, revokedRun.Run.ID, agent, AgentResultInput{Status: "succeeded", Title: "Revoked", Body: "must not land"}, 403)
	// RLS also protects the nullable run/config columns against direct queries.
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), foreign), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_runs WHERE id=$1`, run).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("cross-tenant run leaked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type (
	Input               = recurrences.Input
	Recurrence          = recurrences.Recurrence
	Definition          = recurrences.Definition
	DefinitionScope     = recurrences.DefinitionScope
	AgentDestination    = recurrences.AgentDestination
	AgentBudgetRequest  = recurrences.AgentBudgetRequest
	AgentBudgetDecision = recurrences.AgentBudgetDecision
	AgentResult         = recurrences.AgentResult
	AgentResultInput    = recurrences.AgentResultInput
	Occurrence          = recurrences.Occurrence
)

const Permission = recurrences.Permission

func digestJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

type agentOwnedFixture struct {
	t               *testing.T
	d               *dbtest.DB
	p               tenant.Principal
	project, parent string
	now             time.Time
	m               *recurrences.Module
	handler         http.Handler
}

func agentOwnedSetup(t *testing.T) *agentOwnedFixture {
	f := &agentOwnedFixture{t: t, d: dbtest.Open(t), p: tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person, Name: "Owner", BrowserSession: true}}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'agent-routines','Agent routines')`, f.p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "owner")
	f.node("project", nil, &f.project)
	f.node("work", &f.project, &f.parent)
	f.tx(func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&f.now) })
	f.m = recurrences.New(f.d.App).WithAgentResultWriter(WriteAgentResultTx)
	mux := http.NewServeMux()
	f.m.Mount(mux)
	f.handler = mux
	return f
}
func (f *agentOwnedFixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.p.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *agentOwnedFixture) node(kind string, parent *string, id *string) {
	f.t.Helper()
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Routine fixture',$2 FROM node_kinds WHERE slug=$3 RETURNING id::text`, f.p.TenantID, parent, kind).Scan(id)
	})
}
func (f *agentOwnedFixture) input() Input {
	day := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[f.now.UTC().Weekday()]
	return Input{ProjectID: f.project, ParentID: f.parent, Template: recurrences.Template{Title: "Weekly work", Description: "Bounded research", Type: "work", Priority: "medium"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=" + day, TimeOfDay: "00:00", Timezone: "UTC", StartDate: f.now.UTC().AddDate(0, 0, -7).Format(time.DateOnly)}}
}
func (f *agentOwnedFixture) call(p tenant.Principal, method, path string, body any, status int) []byte {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, req)
	if out.Code != status {
		f.t.Fatalf("%s %s: %d %s want %d", method, path, out.Code, out.Body.String(), status)
	}
	return out.Body.Bytes()
}
func (f *agentOwnedFixture) create(in Input) Recurrence {
	f.t.Helper()
	var r Recurrence
	if err := json.Unmarshal(f.call(f.p, "POST", "/api/recurrences", in, 201), &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *agentOwnedFixture) get(id string) Recurrence {
	f.t.Helper()
	var r Recurrence
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/recurrences/"+id, nil, 200), &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *agentOwnedFixture) manual(id, key string) Occurrence {
	f.t.Helper()
	var o Occurrence
	if err := json.Unmarshal(f.call(f.p, "POST", "/api/recurrences/"+id+"/run-now", map[string]string{"idempotency_key": key}, 200), &o); err != nil {
		f.t.Fatal(err)
	}
	return o
}
func (f *agentOwnedFixture) run() {
	f.t.Helper()
	if err := f.m.RunTenant(f.t.Context(), f.p.TenantID); err != nil {
		f.t.Fatal(err)
	}
}
func (f *agentOwnedFixture) receipts(id string) []Occurrence {
	f.t.Helper()
	var out []Occurrence
	f.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(f.t.Context(), `SELECT node_id::text FROM recurrence_occurrences WHERE recurrence_id=$1 ORDER BY number`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o Occurrence
			if err := rows.Scan(&o.NodeID); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out
}
