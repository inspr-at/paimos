// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func fairQueue(t *testing.T, f *harnessFixture, at time.Time) string {
	t.Helper()
	workorders.New(f.db.App).Mount(f.mux)
	agentruns.New(f.db.App).Mount(f.mux)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"estimate_hours":1,"acceptance_criteria":["A real acceptance criterion"]}'::jsonb WHERE id=$1`, f.ticket)
		return err
	})
	w := f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": f.ticket}, "")
	expect(t, w, 200)
	run := decode(t, w)["queued"].(map[string]any)["run_id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, run, at)
		return err
	})
	return run
}

func fairWorking(t *testing.T, f *harnessFixture) (string, string, map[string]any) {
	t.Helper()
	session, lease, _ := leadCandidate(t, f)
	l := startLead(t, f, 0)
	l = claimLead(t, f, session, lease, l["revision"])
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/heartbeat", map[string]any{"phase": "working", "activity": "idle", "activity_sequence": 1}, lease), 200)
	return session, lease, l
}

func TestLeadIdleYieldCountsUntilConfirmedExit(t *testing.T) {
	f := leadFixture(t, readyLeadChecks)
	session, lease, l := fairWorking(t, f)
	views.New(f.db.App).Mount(f.mux)
	base := "/api/projects/" + f.project
	body := map[string]any{"expected_revision": l["revision"], "generation": l["generation"]}
	expect(t, f.call(f.person, "POST", base+"/lead/yield", body, ""), 403)
	expect(t, f.call(f.agent, "POST", base+"/lead/yield", body, "wrong-private-generation-proof-00000"), 403)
	w := f.call(f.agent, "POST", base+"/lead/yield", body, lease)
	expect(t, w, 200)
	l = decode(t, w)
	if l["reason"] != "idle_yield" || l["state"] != "paused" || l["process_active"] != true {
		t.Fatalf("yield=%v", l)
	}
	if decode(t, f.call(f.person, "GET", "/api/agents/plan", nil, ""))["running_total"] != float64(1) {
		t.Fatal("checkpoint request freed an active process")
	}
	body["expected_revision"] = l["revision"]
	expect(t, f.call(f.agent, "POST", base+"/lead/yield", body, lease), 200)
	var control string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id' FROM harness_sessions WHERE id=$1`, session).Scan(&control)
	})
	finishPaused(t, f, base+"/harness-sessions/"+session, lease, control)
	if decode(t, f.call(f.person, "GET", "/api/agents/plan", nil, ""))["running_total"] != float64(0) {
		t.Fatal("confirmed exit retained capacity")
	}
	next, nextLease, _ := leadCandidate(t, f)
	w = f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"session_id": next, "expected_revision": l["revision"]}, nextLease)
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "explicit start request") {
		t.Fatal("restart bypassed explicit start", w.Body.String())
	}
}

func TestLeadYieldRefusesUsefulTurnAndRevokedOwner(t *testing.T) {
	f := leadFixture(t, readyLeadChecks)
	_, lease, l := fairWorking(t, f)
	fairQueue(t, f, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	path := "/api/projects/" + f.project + "/lead/yield"
	body := map[string]any{"expected_revision": l["revision"], "generation": l["generation"]}
	w := f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "useful current turn") {
		t.Fatal("wrong yield refusal", w.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	expect(t, f.call(f.agent, "POST", path, body, lease), 403)
}

func TestLeadWorkerPriorityYieldRetainsExactAssignment(t *testing.T) {
	f := leadFixture(t, readyLeadChecks)
	session, lease, l := fairWorking(t, f)
	run := fairQueue(t, f, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	binding, _ := json.Marshal(map[string]any{"session_id": session, "generation": 1})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_routed_at=clock_timestamp(),trace=jsonb_set(trace,'{project_lead}',$2::jsonb) WHERE id=$1`, run, binding)
		return err
	})
	base := "/api/projects/" + f.project
	w := f.call(f.agent, "POST", base+"/lead/yield", map[string]any{"expected_revision": l["revision"], "generation": 1}, lease)
	expect(t, w, 200)
	l = decode(t, w)
	if l["reason"] != "worker_priority" {
		t.Fatal("wrong yield", l)
	}
	assignment := func(want string) {
		t.Helper()
		err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
			return harness.RequireAssignedLeadTx(t.Context(), tx, f.agent, f.project, binding)
		})
		if want == "" && err != nil {
			t.Fatal(err)
		}
		if want != "" && (err == nil || err.Error() != want) {
			t.Fatalf("assignment error=%v want=%s", err, want)
		}
	}
	assignment("assignment lead generation is no longer current")
	var control string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id' FROM harness_sessions WHERE id=$1`, session).Scan(&control)
	})
	finishPaused(t, f, base+"/harness-sessions/"+session, lease, control)
	assignment("")
	l = startLead(t, f, int(l["revision"].(float64)))
	assignment("") // An explicit restart request must not strand routed workers.
	// Person pause cancels this scheduling exception even after checkpoint exit.
	expect(t, f.call(f.person, "POST", base+"/lead/pause", map[string]any{"expected_revision": l["revision"], "generation": 1}, ""), 200)
	assignment("assignment lead generation is no longer current")
}

func TestLeadFairTurnsAndUnavailableContender(t *testing.T) {
	mode := "ready"
	admission := func(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner string, s harness.Session) (harness.LeadChecks, error) {
		checks, err := readyLeadChecks(ctx, tx, p, owner, s)
		if s.Host == "other" && mode == "stale" {
			checks.Host.CheckedAt = checks.Host.CheckedAt.Add(-time.Minute)
		}
		return checks, err
	}
	f := leadFixture(t, admission)
	_, _, _ = fairWorking(t, f)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := fairQueue(t, f, at)
	// A second project shares the owner and tenant but has its own generation.
	other, ticket, session := uid(), uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'FAIR-1',id,'Other' FROM node_kinds WHERE slug='project'`, f.person.TenantID, other); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id,fields) SELECT $1,$2,'FAIR-2',id,'Other work',$3,'{"estimate_hours":1,"acceptance_criteria":["B"]}' FROM node_kinds WHERE slug='work'`, f.person.TenantID, ticket, other); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) SELECT tenant_id,$2,$3,agent_principal_id,owner_principal_id,harness,'other',management,role,'working',clock_timestamp(),decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex') FROM harness_sessions WHERE project_id=$1`, f.project, session, other); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, other, f.person.ID, session)
		return err
	})
	w := f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": ticket}, "")
	expect(t, w, 200)
	otherRun := decode(t, w)["queued"].(map[string]any)["run_id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, otherRun, at.Add(time.Minute))
		return err
	})
	turn := func(project, run, want string) {
		t.Helper()
		err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
			reason, err := harness.LeadDispatchTurnTx(t.Context(), tx, f.agent, project, run, admission)
			if reason != want {
				t.Errorf("turn=%s want=%s", reason, want)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	turn(f.project, run, "")
	turn(other, otherRun, "project_turn_wait")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET last_turn_at=$2 WHERE project_id=$1`, f.project, at.Add(2*time.Minute))
		return err
	})
	turn(f.project, run, "project_turn_wait")
	turn(other, otherRun, "")
	mode = "stale"
	turn(f.project, run, "") // Unreadable host cannot become a blocking contender.
	// The internal owner scan must restore caller project visibility.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.visible_projects',$1,true)`, "{"+f.project+"}"); err != nil {
			return err
		}
		_, err := harness.LeadDispatchTurnTx(t.Context(), tx, f.agent, f.project, run, admission)
		if err != nil {
			return err
		}
		var prior string
		if err = tx.QueryRow(t.Context(), `SELECT current_setting('aeon.visible_projects')`).Scan(&prior); err != nil {
			return err
		}
		if prior != "{"+f.project+"}" {
			t.Fatal("scheduling exposed hidden project visibility")
		}
		return nil
	})
}

func TestLeadSchedulingBoundAndDependencies(t *testing.T) {
	f := leadFixture(t, readyLeadChecks)
	_, _, _ = fairWorking(t, f)
	run := fairQueue(t, f, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	blocked := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'DEP-1',id,'Dependency',$3 FROM node_kinds WHERE slug='work'`, f.person.TenantID, blocked, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'blocks')`, f.person.TenantID, blocked, f.ticket)
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		reason, err := harness.LeadDispatchTurnTx(t.Context(), tx, f.agent, f.project, run, readyLeadChecks)
		if reason != "work_not_eligible" {
			t.Fatalf("unmet dependency won a turn: %s", reason)
		}
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE id=$1`, blocked)
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		reason, err := harness.LeadDispatchTurnTx(t.Context(), tx, f.agent, f.project, run, readyLeadChecks)
		if reason != "" {
			t.Fatal("completed dependency blocked ready work", reason)
		}
		return err
	})
	// Clone 200 independent queued leaves. Retain the original asserted run.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH added AS (
 INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
 SELECT $1,'BOUND-'||i,k.id,'Bounded demand',$2,'{"estimate_hours":1,"acceptance_criteria":["B"]}'::jsonb
 FROM generate_series(1,200) i CROSS JOIN node_kinds k WHERE k.slug='work' RETURNING id
 ) INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required,trace)
 SELECT r.tenant_id,r.work_order_id,r.agent_principal_id,added.id,r.queue_by_principal_id,r.queue_at,false,'{}'::jsonb
 FROM added CROSS JOIN agent_runs r WHERE r.id=$3`, f.person.TenantID, f.project, run)
		return err
	})
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
		_, err := harness.LeadDispatchTurnTx(t.Context(), tx, f.agent, f.project, run, readyLeadChecks)
		return err
	})
	if err == nil || err.Error() != "lead scheduling snapshot exceeds bound" {
		t.Fatalf("partial snapshot authorized a turn: %v", err)
	}
}

func TestLeadSingleDialProgressAfterWorkerYield(t *testing.T) {
	admission := func(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner string, s harness.Session) (harness.LeadChecks, error) {
		checks, err := readyLeadChecks(ctx, tx, p, owner, s)
		if err != nil {
			return checks, err
		}
		if s.RunID != nil {
			var raw []byte
			var running int
			if err = tx.QueryRow(ctx, `SELECT value FROM user_preferences WHERE principal_id=$1 AND key=$2`, owner, agentplan.PreferenceKey).Scan(&raw); err != nil {
				return checks, err
			}
			plan, _, err := agentplan.Decode(raw)
			if err != nil {
				return checks, err
			}
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM harness_sessions WHERE owner_principal_id=$1 AND NOT aeon_work_session_stopped(stopped_at,stop_reason)`, owner).Scan(&running); err != nil {
				return checks, err
			}
			if ok, _ := agentplan.CanStart(plan, map[string]int{"codex": running}, "codex"); !ok {
				checks.Dial.State = "full"
			}
		}
		return checks, nil
	}
	f := leadFixture(t, admission)
	session, lease, l := fairWorking(t, f)
	run := fairQueue(t, f, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	other, otherSession, profile := uid(), uid(), uid()
	binding, _ := json.Marshal(map[string]any{"session_id": session, "generation": 1})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,$3,'{"total":2,"limits":{}}')`, f.person.TenantID, f.person.ID, agentplan.PreferenceKey); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'DIAL-1',id,'Other lead' FROM node_kinds WHERE slug='project'`, f.person.TenantID, other); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,ref_digest,lease_digest) SELECT tenant_id,$2,$3,agent_principal_id,owner_principal_id,harness,host,management,role,phase,decode(repeat('ef',32),'hex'),decode(repeat('fa',32),'hex') FROM harness_sessions WHERE id=$1`, session, otherSession, other); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, other, f.person.ID, otherSession); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'dial-worker','1','codex','openai','test-model','high','strong')`, f.person.TenantID, profile); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET agent_principal_id=$2,model_profile_id=$3,queue_routed_at=clock_timestamp(),trace=jsonb_set(trace,'{project_lead}',$4::jsonb) WHERE id=$1`, run, f.agent.ID, profile, binding)
		return err
	})
	workerStart := func(want string) {
		t.Helper()
		err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
			return harness.RequireAssignedLeadStartTx(t.Context(), tx, f.agent, f.project, run, binding, admission)
		})
		if want == "" && err != nil || want != "" && (err == nil || err.Error() != want) {
			t.Fatalf("start=%v want=%s", err, want)
		}
	}
	workerStart("worker admission wait: dial_full") // N=2 live leads, dial=2.
	base := "/api/projects/" + f.project
	w := f.call(f.agent, "POST", base+"/lead/yield", map[string]any{"expected_revision": l["revision"], "generation": 1}, lease)
	expect(t, w, 200)
	workerStart("assignment lead generation is no longer current") // Request is not exit.
	var control string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id' FROM harness_sessions WHERE id=$1`, session).Scan(&control)
	})
	finishPaused(t, f, base+"/harness-sessions/"+session, lease, control)
	workerStart("") // One still-live lead plus one prospective worker fits the SAME dial.
	var ownerCalls int
	stale := func(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner string, s harness.Session) (harness.LeadChecks, error) {
		ownerCalls++
		checks, err := admission(ctx, tx, p, owner, s)
		checks.Host.State = "unavailable"
		return checks, err
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
		return harness.RequireAssignedLeadStartTx(t.Context(), tx, f.agent, f.project, run, binding, stale)
	})
	if err == nil || err.Error() != "worker admission wait: host_unavailable" || ownerCalls != 1 {
		t.Fatalf("yield bypassed live host gate: %v calls=%d", err, ownerCalls)
	}
}
