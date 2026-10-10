// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Risks: preparing the wrong/unconsented work, hidden dependency bypass,
// replacing accepted criteria, incomplete scans reported ready and a second
// writer racing a person pickup. Reuse queue/consent guards; the pickup proof
// uses a PostgreSQL lock barrier, and cancellation is injected without sleeps.
func TestRoutinePreparationPreservesAuthorityCriteriaAndPickup(t *testing.T) {
	f := setup(t)
	f.person.BrowserSession = true
	var project, parent, kind string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Preparation project','{"project_key":"PRE"}' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Routine output',$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, project).Scan(&parent); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.person.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL`).Scan(&kind); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,$2,$3,'{}',$3)`, f.person.TenantID, project, f.person.ID)
		return err
	})
	q := modelregistry.Qualification{ID: uuid(), ProjectID: project, Runtime: modelregistry.ExecutionRuntime{ServerDigest: strings.Repeat("a", 64), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), Capabilities: []string{"routine_native_coding_v1"}, HostMappingDigest: strings.Repeat("d", 64), BudgetModes: []string{"off"}}, CoordinatorAcceptance: strings.Repeat("e", 64), OPSAttestation: strings.Repeat("f", 64)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var err error
		q.OwnerPersonID, q.PolicyDigest, err = modelregistry.QualificationPolicyTx(t.Context(), tx, f.person.TenantID, project)
		return err
	})
	runtime := func(ctx context.Context, tx pgx.Tx, _ string) (modelregistry.ExecutionRuntime, error) {
		r := q.Runtime
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&r.ObservedAt)
		return r, err
	}
	recurrences.New(f.d.App).WithExecutionRuntime(runtime).Mount(f.mux)
	in := recurrences.Input{ProjectID: project, ParentID: parent, QueueEach: true, OverlapPolicy: "create", CatchUpPolicy: "one", Template: recurrences.Template{Title: "Prepare {{occurrence}}", Description: "Read assigned work", Criteria: []string{"Keep accepted criteria"}, EstimateHours: 2, Priority: "high", Type: "work"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "Europe/Vienna"}, Definition: &recurrences.Definition{Scope: recurrences.DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.person.ID, Assignment: &recurrences.Assignment{Goal: "Prepare authorized work", Role: "build", WorkKindID: kind, AllowedActions: []string{"work.update"}, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: recurrences.DefinitionBudget{Mode: "off"}}}}
	var routine recurrences.Recurrence
	f.call(t, f.person, "POST", "/api/recurrences", in, 201, &routine)
	input := agentruns.RoutinePreparationInput{RecurrenceID: routine.ID, DefinitionRevision: routine.Revision}
	prepare := func() agentruns.RoutinePreparation {
		t.Helper()
		var out agentruns.RoutinePreparation
		ctx := tenant.WithPrincipal(t.Context(), f.person)
		if err := db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = agentruns.PrepareRoutineWorkTx(ctx, tx, f.person, input, runtime)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := prepare(); got.Candidate != nil || got.WaitReason != "automatic_launch_disabled" || got.Partial {
		t.Fatalf("default-off preparation: %+v", got)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error { return modelregistry.RecordQualificationTx(t.Context(), tx, f.person, q) })
	revision, enabled := int64(0), true
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.person, project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &revision, AutomaticLaunchEnabled: &enabled, QualificationID: &q.ID}, runtime)
		return err
	})
	path := "/api/recurrences/" + routine.ID
	f.call(t, f.person, "POST", path+"/resume", map[string]any{"expected_revision": routine.Revision}, 200, &routine)
	input.DefinitionRevision = routine.Revision
	if got := prepare(); got.Candidate != nil || got.WaitReason != "execution_consent_required" {
		t.Fatalf("queue without consent: %+v", got)
	}
	f.call(t, f.person, "PUT", path+"/execution-consent", map[string]any{"expected_revision": routine.Revision, "execute_consent": true}, 200, &routine)
	input.DefinitionRevision = routine.Revision
	var first, second recurrences.Occurrence
	f.call(t, f.person, "POST", path+"/run-now", map[string]string{"idempotency_key": "first"}, 200, &first)
	f.call(t, f.person, "POST", path+"/run-now", map[string]string{"idempotency_key": "second"}, 200, &second)
	unrelated := f.ticket(t, "open", "urgent", nil)
	f.addQueue(t, unrelated, nil)
	assertCandidate := func(want string) agentruns.RoutinePreparedWork {
		t.Helper()
		got := prepare()
		if got.Partial || got.Candidate == nil || got.Candidate.NodeID != want || got.WaitReason != "" {
			t.Fatalf("preparation selected wrong work: %+v, want %s", got, want)
		}
		return *got.Candidate
	}
	assertCandidate(*first.NodeID)
	// Manual queue order and targeted Start now are retained, even though an
	// unrelated urgent backlog entry exists. Preparation itself writes no route.
	f.call(t, f.person, "POST", "/api/queue/"+*second.NodeID+"/move", map[string]int{"position": 1}, 200, nil)
	assertCandidate(*second.NodeID)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_target_agent_id=$2 WHERE id=$1`, *first.Run.AgentRunID, f.agent.ID)
		return err
	})
	work := assertCandidate(*first.NodeID)
	input.TargetAgentID = f.other.ID
	assertCandidate(*second.NodeID)
	input.TargetAgentID = ""
	input.CandidateLimit = 1
	if got := prepare(); !got.Partial || got.Candidate != nil || got.WaitReason != "candidate_scan_partial" {
		t.Fatalf("incomplete scan ready: %+v", got)
	}
	input.CandidateLimit = 0
	// Changes and rejection are drafts only; replay cannot rewrite prior criteria.
	criteria := []string{"Changed criteria need acceptance"}
	for range 2 {
		draft, err := agentruns.DraftRoutineCriteria(work, criteria)
		if err != nil || !draft.NeedsPerson || draft.NodeID != work.NodeID || !draft.ExpectedRevision.Equal(work.ExpectedRevision) || draft.CriteriaDigest != work.CriteriaDigest {
			t.Fatalf("unbound draft: %+v %v", draft, err)
		}
		assertCandidate(*first.NodeID)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields-'estimate_hours',updated_at=clock_timestamp() WHERE id=$1`, *first.NodeID)
		return err
	})
	missing := prepare()
	if missing.Candidate == nil || missing.Candidate.Estimate == nil || missing.Candidate.CriteriaDigest != work.CriteriaDigest || missing.WaitReason != "estimate_action_required" {
		t.Fatalf("estimate changed criteria identity: %+v", missing)
	}
	f.call(t, f.person, "POST", "/api/queue/"+*first.NodeID+"/estimate", map[string]float64{"estimate_hours": missing.Candidate.Estimate.EstimateHours}, 200, nil)
	if next := assertCandidate(*first.NodeID); next.CriteriaDigest != work.CriteriaDigest {
		t.Fatal("typed estimate replaced accepted criteria")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields-'acceptance_criteria',updated_at=clock_timestamp() WHERE id=$1`, *first.NodeID)
		return err
	})
	missing = prepare()
	if missing.WaitReason != "criteria_wait" || missing.Candidate == nil || missing.Candidate.Estimate != nil {
		t.Fatalf("new criteria did not wait: %+v", missing)
	}
	if _, err := agentruns.DraftRoutineCriteria(*missing.Candidate, criteria); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return agentruns.RequireRoutinePreparationTx(t.Context(), tx, f.person, input, *missing.Candidate, runtime)
	})
	var criteriaRefusal *workorders.Error
	if !errors.As(err, &criteriaRefusal) || criteriaRefusal.Status != 409 || criteriaRefusal.Message != "criteria_wait" {
		t.Fatalf("missing criteria passed the final write guard: %v", err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{acceptance_criteria}','["Keep accepted criteria"]'),body=repeat('a',65537),updated_at=clock_timestamp() WHERE id=$1`, *first.NodeID)
		return err
	})
	if got := prepare(); !got.Partial || got.Candidate != nil || got.WaitReason != "preparation_fields_partial" {
		t.Fatalf("field limit not honest: %+v", got)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET body='',updated_at=clock_timestamp() WHERE id=$1`, *first.NodeID)
		return err
	})
	// Live blockers exclude even open work. RLS-hidden relation sources keep
	// the check closed and its widened savepoint restores the original scope.
	blocker := f.ticket(t, "open", "low", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'blocks')`, f.person.TenantID, blocker, *first.NodeID)
		return err
	})
	assertCandidate(*second.NodeID)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='accepted' WHERE id=$1`, blocker)
		return err
	})
	assertCandidate(*first.NodeID)
	var hiddenProject string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Hidden blocker project','{"project_key":"HBP"}' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&hiddenProject); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, blocker, hiddenProject)
		return err
	})
	// Restrict visibility to the output project; even the closed hidden source
	// remains unavailable, without exposing its identity in the returned reason.
	visibility := "{" + project + "}"
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.visible_projects',$1,true)`, visibility); err != nil {
			return err
		}
		wait, err := workqueue.BlockingTx(t.Context(), tx, *first.NodeID)
		if err != nil {
			return err
		}
		if wait != "blocker_unavailable" {
			t.Fatalf("hidden blocker bypass: %q", wait)
		}
		var visible string
		if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.visible_projects')`).Scan(&visible); err != nil {
			return err
		}
		if visible != visibility {
			t.Fatal("blocker scan leaked widened project scope")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Too many relation edges is an incomplete scan even if all are closed.
	var extraBlockers []string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Closed dependency','done',$2 FROM node_kinds k CROSS JOIN generate_series(1,64) WHERE k.slug='work' RETURNING id::text`, f.person.TenantID, hiddenProject)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			extraBlockers = append(extraBlockers, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) SELECT $1,unnest($2::uuid[]),$3,'blocks'`, f.person.TenantID, extraBlockers, *first.NodeID)
		return err
	})
	if got := prepare(); !got.Partial || got.Candidate != nil || got.WaitReason != "blocker_scan_partial" {
		t.Fatalf("blocker scan exceeded budget: %+v", got)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM node_relations WHERE source_node_id=ANY($1::uuid[]) AND target_node_id=$2`, extraBlockers, *first.NodeID)
		return err
	})
	// An injected expired deadline proves the explicit incomplete result.
	expired, cancel := context.WithDeadline(tenant.WithPrincipal(t.Context(), f.person), time.Unix(0, 0))
	defer cancel()
	var timed agentruns.RoutinePreparation
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		var err error
		timed, err = agentruns.PrepareRoutineWorkTx(expired, tx, f.person, input, runtime)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || !timed.Partial || timed.Candidate != nil || timed.WaitReason != "preparation_time_limit" {
		t.Fatalf("timeout dishonest: %+v %v", timed, err)
	}
	work = assertCandidate(*first.NodeID)
	// Human pickup holds the real access fence while the final preparation
	// guard waits behind it. After commit, the stale preparation cannot proceed.
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), f.person), 15*time.Second)
	defer cancel()
	holder, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(t.Context())
	if _, err = holder.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if err = db.LockWorkTreeTx(ctx, holder); err != nil {
		t.Fatal(err)
	}
	if _, err = holder.Exec(ctx, `UPDATE nodes SET state='in_progress',fields=jsonb_set(fields,'{assignee}',to_jsonb($2::text)),updated_at=clock_timestamp() WHERE id=$1`, *first.NodeID, f.person.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			return agentruns.RequireRoutinePreparationTx(ctx, tx, f.person, input, work, runtime)
		})
	}()
	dbtest.WaitForLock(t, ctx, f.d, holder.Conn().PgConn().PID(), "transactionid")
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = dbtest.Await(t, ctx, done)
	var refusal *workorders.Error
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != "stale_preparation" {
		t.Fatalf("pickup race refused for wrong reason: %v", err)
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, *first.NodeID); n != 1 {
		t.Fatalf("preparation made %d assignments", n)
	}
}

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
 SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Work',$2,$3 FROM node_kinds k WHERE k.slug='work' RETURNING id::text`, f.person.TenantID, state, raw).Scan(&id)
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

// Mutation responses feed the row store directly. No GET/readiness reload may
// be required between an ordinary edit and queueing idle In progress work.
func TestStaleQueueAfterNodeMutation(t *testing.T) {
	for _, change := range []string{"title", "body", "fields", "estimate", "same_kind", "move", "convert", "create", "unassign", "bulk_unassign", "bulk_priority"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			fields := map[string]any{"estimate_hours": 2, "acceptance_criteria": "- [ ] tests pass"}
			if change == "unassign" || change == "bulk_unassign" {
				fields["assignee"] = f.person.ID
			}
			id := f.ticket(t, "in_progress", "high", fields)
			if change == "convert" {
				// A tenant-defined legacy kind converts into canonical work.
				f.tx(t, f.person, func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,field_schema) VALUES($1,'task','Task','TSK','task','{"type":"object","issue_family":true}')`, f.person.TenantID); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE slug='task') WHERE id=$1`, id)
					return err
				})
			}
			if change == "unassign" || change == "bulk_unassign" {
				var ready struct{ Queueable, Stale bool }
				f.call(t, f.person, "GET", "/api/queue/"+id+"/readiness", nil, 200, &ready)
				if ready.Queueable || ready.Stale {
					t.Fatal("assigned fixture must be ineligible before unassignment")
				}
			}
			var kindID string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT kind_id::text FROM nodes WHERE id=$1`, id).Scan(&kindID)
			})
			method, path, status := "PATCH", "/api/nodes/"+id, 200
			var patch any
			switch change {
			case "title":
				patch = map[string]any{"title": "Edited idle work"}
			case "body":
				patch = map[string]any{"body": "Edited description"}
			case "fields":
				patch = map[string]any{"fields": map[string]any{"priority": "low", "estimate_hours": 2, "acceptance_criteria": "- [ ] tests pass"}}
			case "unassign":
				patch = map[string]any{"fields": map[string]any{"assignee": nil, "priority": "high", "estimate_hours": 2, "acceptance_criteria": "- [ ] tests pass"}}
			case "bulk_unassign":
				method, path = "POST", "/api/nodes/bulk"
				patch = map[string]any{"ids": []string{id}, "assignee": nil}
			case "bulk_priority":
				method, path = "POST", "/api/nodes/bulk"
				patch = map[string]any{"ids": []string{id}, "priority": "low"}
			case "estimate":
				patch = map[string]any{"estimate_hours": 3}
			case "same_kind":
				patch = map[string]any{"kind_id": kindID}
			case "move":
				method, path, patch = "POST", path+"/move", map[string]any{"parent_id": nil}
			case "convert":
				method, path, patch = "POST", path+"/convert", map[string]any{"to_kind": "work"}
			case "create":
				method, path, status = "POST", "/api/nodes", 201
				patch = map[string]any{"kind_id": kindID, "title": "New idle work", "state": "in_progress", "fields": map[string]any{"estimate_hours": 2, "acceptance_criteria": "- [ ] tests pass"}}
			}
			type mutationNode struct {
				ID         string
				State      string
				QueueStale bool `json:"queue_stale"`
			}
			var node mutationNode
			if path == "/api/nodes/bulk" {
				var result struct {
					Items     []mutationNode
					Unchanged []string
					Skipped   []any
				}
				f.call(t, f.person, method, path, patch, status, &result)
				if len(result.Items) != 1 || len(result.Unchanged) != 0 || len(result.Skipped) != 0 || result.Items[0].ID != id {
					t.Fatalf("bulk mutation did not change exactly its target: %+v", result)
				}
				node = result.Items[0]
			} else {
				f.call(t, f.person, method, path, patch, status, &node)
			}
			if node.State != "in_progress" || !node.QueueStale {
				t.Fatalf("mutation cleared idle-work eligibility: %+v", node)
			}
			e := f.addQueue(t, node.ID, nil)
			if e.State != "open" || e.Queued == nil || e.Undo == nil {
				t.Fatalf("queue directly after mutation: %+v", e)
			}
			var queuedEdit struct {
				QueueStale bool `json:"queue_stale"`
				Queued     *workqueue.Queued
			}
			if path == "/api/nodes/bulk" {
				var result struct {
					Items []struct {
						QueueStale bool `json:"queue_stale"`
						Queued     *workqueue.Queued
					}
				}
				f.call(t, f.person, "POST", path, map[string]any{"ids": []string{node.ID}, "priority": "medium"}, 200, &result)
				if len(result.Items) != 1 {
					t.Fatalf("queued bulk edit missing mutation response: %+v", result)
				}
				queuedEdit = result.Items[0]
			} else {
				f.call(t, f.person, "PATCH", "/api/nodes/"+node.ID, map[string]any{"title": "Edited queued work"}, 200, &queuedEdit)
			}
			if queuedEdit.QueueStale || queuedEdit.Queued == nil || queuedEdit.Queued.RunID != e.Queued.RunID {
				t.Fatalf("queued mutation lost current queue projection: %+v", queuedEdit)
			}
		})
	}
}

func TestStaleQueueUndoFences(t *testing.T) {
	for _, reason := range []string{"revision", "assignment", "session", "started", "replaced", "permission", "actor", "foreign"} {
		t.Run(reason, func(t *testing.T) {
			f := setup(t)
			id := f.ticket(t, "in_progress", "high", nil)
			e := f.addQueue(t, id, nil)
			if e.Undo == nil {
				t.Fatal("missing Undo token")
			}
			p, status := f.person, 409
			switch reason {
			case "revision":
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Changed',updated_at=updated_at+interval '1 microsecond' WHERE id=$1`, id)
					return err
				})
			case "assignment":
				// Intentionally preserve updated_at so the idle guard, rather
				// than the earlier revision guard, must refuse this Undo.
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('assignee',$2::text) WHERE id=$1`, id, f.person.ID)
					return err
				})
			case "session":
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var project string
					if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project','open' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
						return err
					}
					// A live binding is independent of the ticket revision.
					_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase)
 VALUES($1,$2,$3,$4,'codex','local','unmanaged','worker','ship',$5,decode(repeat('cd',32),'hex'),'working')`, f.person.TenantID, project, f.agent.ID, id, []byte(id[:32]))
					return err
				})
			case "started":
				// A started run needs a real allowed account/reservation. Keep
				// this fixture valid under the activated account-use boundary.
				f.reserve(t, agentruns.Run{ID: e.Undo.RunID})
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
			var state, runStatus, revision string
			var queuedNode *string
			var fields json.RawMessage
			var undos int
			inspect := func() {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT n.state,n.updated_at::text,n.fields,r.status,r.queue_node_id::text,
 (SELECT count(*) FROM events WHERE node_id=n.id AND type='queue.add_undone')
 FROM nodes n JOIN agent_runs r ON r.id=$2 WHERE n.id=$1`, id, e.Undo.RunID).Scan(&state, &revision, &fields, &runStatus, &queuedNode, &undos)
				})
			}
			inspect()
			beforeRevision, beforeFields := revision, string(fields)
			if reason == "assignment" || reason == "session" {
				var unchanged bool
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT updated_at=$2::timestamptz FROM nodes WHERE id=$1`, id, e.Undo.Revision).Scan(&unchanged)
				})
				if !unchanged {
					t.Fatal("fixture must preserve the queue revision to isolate the idle guard")
				}
			}
			var refusal struct {
				Error string `json:"error"`
			}
			f.call(t, p, "POST", "/api/queue/"+id+"/undo", e.Undo, status, &refusal)
			if (reason == "assignment" || reason == "session") && refusal.Error != "ticket is no longer idle" {
				t.Fatalf("Undo hit the wrong guard: %q", refusal.Error)
			}
			inspect()
			if state != "open" || revision != beforeRevision || string(fields) != beforeFields || undos != 0 {
				t.Fatalf("refused Undo changed ticket/audit: state=%s revision=%s fields=%s undos=%d", state, revision, fields, undos)
			}
			if reason != "started" && reason != "replaced" && (runStatus != "queued" || queuedNode == nil || *queuedNode != id) {
				t.Fatalf("refused Undo lost queued run: status=%s ticket=%v", runStatus, queuedNode)
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
 VALUES($1,$2,$3,$4,'codex','local','unmanaged','worker','ship',$6,decode(repeat('cd',32),'hex'),CASE WHEN $5 THEN 'stopped' ELSE 'working' END,CASE WHEN $5 THEN now() END,CASE WHEN $5 THEN 'process_exited' END)`, f.person.TenantID, project, f.agent.ID, id, stopped, []byte(id[:32]))
			return err
		})
		// Use the harness's canonical confirmed-exit reason, and prove that
		// the fixture establishes the same exit fence enforced by mutation.
		confirmed := f.count(t, f.person, `SELECT count(*) FROM harness_sessions WHERE ticket_node_id=$1 AND aeon_work_session_stopped(stopped_at,stop_reason)`, id)
		if (confirmed == 1) != stopped {
			t.Fatal("fixture did not establish the expected session exit evidence")
		}
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
