// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

func TestDeliveryFlowCheckSuitesFillLinkedChanges(t *testing.T) {
	// Risk: the least-privileged GitHub feed contains only suites, so linked
	// PRs stay empty, or inferred attempts and queue waits look like exact facts.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	auditSend(t, f, "pull_request", "suite-fallback-link", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": f.at.Add(-2 * time.Hour), "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	g.source = historyChecks
	started := f.at.Add(-time.Hour)
	g.respond = func(endpoint string) (any, error) {
		if strings.HasPrefix(endpoint, "/actions/") {
			t.Fatalf("suite fallback requested new Actions authority: %s", endpoint)
		}
		switch {
		case strings.HasPrefix(endpoint, "/check-suites/900/check-runs"):
			return historyChecksBody(900, p.Head, started, false), nil
		case endpoint == "/check-suites/900":
			return historySuiteBody(900, p.Head, p.Branch, started, false), nil
		case strings.HasPrefix(endpoint, "/contents/"):
			if strings.HasPrefix(endpoint, "/contents/.github/workflows?") {
				return []any{map[string]any{"path": defaultCIWorkflow, "type": "file", "sha": strings.Repeat("c", 40)}}, nil
			}
			return historyWorkflowBody(defaultCIWorkflow, ""), nil
		}
		return nil, fmt.Errorf("unexpected suite read %s", endpoint)
	}
	body := map[string]any{"action": "completed", "check_suite": historySuiteBody(900, p.Head, p.Branch, started, false)}
	auditSend(t, f, "check_suite", "suite-fallback", body, 204)
	var run DeliveryFlowRun
	path := "/api/projects/" + f.project + "/delivery/flow/runs/" + flowItemID(f.person.TenantID, f.project, "change", "AEON-848")
	f.call(t, f.person, "GET", path, nil, 200, &run)
	if len(run.Steps) != 1 || len(run.Item.PRs) != 1 || run.Item.PRs[0] != 7 {
		t.Fatalf("check-suite-only change: %+v", run)
	}
	s := run.Steps[0]
	if s.Source != "github_app" || s.Actor.Label != "Checks (derived from check suite)" || s.StepKey != "ci" || s.Round != 1 || s.Kind != "work" || s.WaitReason != nil || s.Outcome == nil || *s.Outcome != "green" || !s.StartedAt.Equal(started) || s.EndedAt == nil {
		t.Fatalf("derived span invented workflow facts: %+v", s)
	}
	before := flowEventCount(t, f)
	auditSend(t, f, "check_suite", "suite-fallback-replay", body, 204)
	if flowEventCount(t, f) != before {
		t.Fatal("suite replay changed Flow")
	}
	// A workflow on another head cannot suppress the fallback for this head.
	pr := int64(7)
	exact := metricRun{ID: 501, Attempt: 1, Workflow: defaultCIWorkflow, Event: "pull_request", Head: strings.Repeat("e", 40), PR: &pr, Created: started, Started: started, Completed: started.Add(time.Minute), Conclusion: "success"}
	f.tx(t, func(tx pgx.Tx) error {
		return upsertRunsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "webhook", []metricRun{exact}, f.at)
	})
	fallback := exact
	fallback.ID, fallback.Head = -901, p.Head
	if err := f.m.flowFromRuns(t.Context(), []metricRun{fallback}); err != nil {
		t.Fatal(err)
	}
	before = flowEventCount(t, f)
	exact.Head, fallback.ID = p.Head, -902
	f.tx(t, func(tx pgx.Tx) error {
		return upsertRunsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "webhook", []metricRun{exact}, f.at)
	})
	if err := f.m.flowFromRuns(t.Context(), []metricRun{fallback}); err != nil || flowEventCount(t, f) != before {
		t.Fatalf("same-head workflow did not suppress fallback: %v", err)
	}
}

func TestDeliveryFlowHarnessProjectsOnceAndPreservesOPSReleases(t *testing.T) {
	// Risk: harness deliveries without work orders stay invisible, replay
	// adds rows/events, or a backfill rewrites OPS-owned releases and incident 135.
	f := newFixture(t)
	path := "/api/projects/" + f.project + "/delivery/flow"
	for release := 131; release <= 136; release++ {
		body := release126(f, f.at.Add(-2*time.Hour), nil)
		body["release"] = fmt.Sprint(release)
		body["version"] = fmt.Sprintf("261016%06d.0.0", release)
		if release != 135 {
			body["incidents"] = []any{}
		}
		var result RolloutResult
		f.call(t, f.person, "POST", path+"/rollout", body, 200, &result)
		if result.ItemID != flowItemID(f.person.TenantID, f.project, "release", fmt.Sprint(release)) {
			t.Fatal("release identity used version instead of label")
		}
		body["version"] = "a-different-version"
		f.call(t, f.person, "POST", path+"/rollout", body, 200, &result)
		if result.Changed != 0 {
			t.Fatalf("OPS release %d replay changed %d rows", release, result.Changed)
		}
		// Omitted step and incident keys stay owned by the original report.
		body["steps"], body["incidents"] = []any{}, []any{}
		before := flowEventCount(t, f)
		f.call(t, f.person, "POST", path+"/rollout", body, 200, &result)
		if result.Changed != 0 || flowEventCount(t, f) != before {
			t.Fatalf("partial OPS release %d replay changed rows or events", release)
		}
	}
	opsSnapshot := func() string {
		t.Helper()
		var snapshot string
		f.tx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_build_array(
			 (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM delivery_flow_items i WHERE kind='release'),
			 (SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM delivery_flow_steps s JOIN delivery_flow_items i ON i.id=s.item_id WHERE i.kind='release'),
			 (SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM delivery_flow_incidents n))::text`).Scan(&snapshot)
		})
		return snapshot
	}
	ops := opsSnapshot()
	if !strings.Contains(ops, "degraded") {
		t.Fatal("fixture lost the incident it promises to protect")
	}
	p := f.pull()
	f.gh.pulls[7] = p
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	start, stop := f.at.Add(-50*time.Minute), f.at.Add(-20*time.Minute)
	author, reviewer, gate := requestID(701), requestID(702), requestID(703)
	f.tx(t, func(tx pgx.Tx) error {
		for _, id := range []string{author, reviewer} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','fixture','unmanaged','worker','ship',$6,$6)`, f.person.TenantID, id, f.project, f.agent.ID, f.ticket, []byte(id)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO review_runs(tenant_id,id,ticket_node_id,project_id,repository,pull_request,target_branch,base_sha,head_sha,scope,kind,author_session_id,reviewer_session_id,reviewer_principal_id,author_family,reviewer_family,harness,model,reasoning_effort,created_by,created_at,expires_at,submitted_at,verdict) VALUES($1,$2,$3,$4,$5,7,'main',$6,$7,$6||'..'||$7,'review',$8,$9,$10,'openai','anthropic','claude','review-model','high',$10,$11,$12,$12,'ok')`, f.person.TenantID, gate, f.ticket, f.project, f.m.config.Repository, p.Base, p.Head, author, reviewer, f.agent.ID, start.Add(10*time.Minute), stop)
		return err
	})
	snap := map[string]any{"id": author, "project_id": f.project, "agent_principal_id": f.agent.ID, "ticket_node_id": f.ticket, "role": "worker", "work_shape": "ship", "created_at": start, "model": "build-model", "finished": false}
	appendFlowSource(t, f, events.Change{NodeID: &f.project, Type: "harness.registered", After: snap, At: &start})
	snap["stopped_at"], snap["finished"] = stop, true
	appendFlowSource(t, f, events.Change{NodeID: &f.project, Type: "harness.stopped", After: snap, At: &stop})
	appendFlowSource(t, f, events.Change{NodeID: &f.ticket, Type: "coordinator_review.recorded", After: map[string]any{"run_id": gate, "ok": true}, At: &stop})
	// A historical, unverified verdict must not become an authenticated gate.
	appendFlowSource(t, f, events.Change{NodeID: &f.ticket, Type: "coordinator_review.recorded", After: map[string]any{"ok": true}, At: &stop})
	appendFlowSource(t, f, events.Change{NodeID: &f.project, Type: "delivery.ship.decision", After: shipSnapshot{Project: f.project, Decision: &ShipDecision{ShipInput: ShipInput{Request: requestID(704)}, Project: f.project, Claimant: f.agent.ID, Repository: f.m.config.Repository, Execute: true, Claimed: true, Action: "enqueue", Facts: ShipFacts{PR: int64ptr(7)}}}, At: &stop})
	meta, _ := json.Marshal(map[string]any{"delivery_item_id": requestID(700), "pull_request": 7, "repository": f.m.config.Repository})
	appendFlowSource(t, f, events.Change{NodeID: &f.ticket, Type: "delivery.state_changed", Before: map[string]any{"state": "in_queue"}, After: map[string]any{"state": "merged"}, Metadata: meta, At: &stop})
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			f.tx(t, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE delivery_flow_cursors SET last_event_id=0`)
				return err
			})
		}
		before := flowEventCount(t, f)
		for {
			more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				break
			}
		}
		if pass == 1 && flowEventCount(t, f) != before {
			t.Fatal("re-projecting harness events changed Flow")
		}
	}
	var run DeliveryFlowRun
	f.call(t, f.person, "GET", path+"/runs/"+flowItemID(f.person.TenantID, f.project, "change", "AEON-848"), nil, 200, &run)
	if len(run.Steps) != 4 || run.Item.PctDone != 100 || run.Item.EndedAt == nil {
		t.Fatalf("harness pipeline: %+v", run)
	}
	for _, s := range run.Steps {
		if s.Source != "paimos" || s.EndedAt == nil {
			t.Fatalf("harness step: %+v", s)
		}
	}
	for key, want := range map[string]string{"harness/" + author: "build/ok", "review_run/" + gate: "review/ok", "ship/" + requestID(704): "queue/-", "merge/" + requestID(700): "queue/green"} {
		step, _, outcome := flowStepRow(t, f, "paimos", key)
		value := "-"
		if outcome != nil {
			value = *outcome
		}
		if step+"/"+value != want {
			t.Fatalf("harness fact %s: %s/%s, want %s", key, step, value, want)
		}
	}
	if opsSnapshot() != ops {
		t.Fatal("harness projector changed an OPS release, omitted step or incident")
	}
}

func TestDeliveryFlowRolloutScopeIsNarrowAndValidated(t *testing.T) {
	// Risk: OPS cannot report with its own scoped key, or that scope permits
	// other delivery management or bypasses rollout validation.
	f := newFixture(t)
	p := f.agent
	p.Scopes = []string{"delivery.rollout.write"}
	f.tx(t, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'rollout_reporter','Rollout reporter') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'delivery.rollout.write')`, p.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, p.TenantID, p.ID, role, f.project)
		return err
	})
	path := "/api/projects/" + f.project + "/delivery/flow/rollout"
	body := release126(f, f.at.Add(-time.Hour), nil)
	var result RolloutResult
	f.call(t, p, "POST", path, body, 200, &result)
	before := flowEventCount(t, f)
	f.call(t, p, "POST", path, body, 200, &result)
	if result.Changed != 0 || flowEventCount(t, f) != before {
		t.Fatal("scoped rollout replay emitted events")
	}
	body["schema"] = "unvalidated"
	f.call(t, p, "POST", path, body, 400, nil)
	f.call(t, p, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{}, 403, nil)
	p.Scopes = []string{"delivery.read"}
	f.call(t, p, "POST", path, release126(f, f.at.Add(-time.Hour), nil), 403, nil)
}

func int64ptr(n int64) *int64 { return &n }

func TestDeliveryFlowBackfillPlans87LinkedPRsWithoutWriting(t *testing.T) {
	// Risk: OPS's one-time plan writes to production in dry-run mode, silently
	// truncates the 87 linked PRs, or a second application duplicates history.
	f := newFixture(t)
	start := f.at.Add(-time.Hour)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title)
		 SELECT $1,$2,'AEON-'||n,k.id,'Linked delivery '||n FROM generate_series(1000,1086) n CROSS JOIN node_kinds k WHERE k.slug='work'`, f.person.TenantID, f.project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,updated_at,observation)
		 SELECT $1,gen_random_uuid(),$2,id,$3,substring(key from 6)::bigint,'work/'||lower(key),repeat('b',40),'pushed',$4,'LEAD',$4,'{}' FROM nodes WHERE parent_id=$2 AND key IN (SELECT 'AEON-'||n FROM generate_series(1000,1086) n)`, f.person.TenantID, f.project, f.m.config.Repository, start); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_runs(tenant_id,repository,run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,pull_request,created_at,started_at,completed_at,conclusion,source,recorded_at)
		 SELECT $1,$2,-pull_request,1,$3,'Observed checks','pull_request',branch,head_sha,pull_request,$4,$4,$5,'success','backfill',$5 FROM delivery_items`, f.person.TenantID, f.m.config.Repository, defaultCIWorkflow, start, f.at)
		return err
	})
	plan, err := f.m.BackfillFlow(t.Context(), f.person, f.project, 87, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.Changed != nil || len(plan.LinkedPRs) != 87 || len(plan.Items) != 87 || flowEventCount(t, f) != 0 {
		t.Fatalf("dry-run plan: %+v", plan)
	}
	var rows int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_flow_items)+(SELECT count(*) FROM delivery_flow_steps)+(SELECT count(*) FROM delivery_flow_cursors)`).Scan(&rows)
	})
	if rows != 0 {
		t.Fatalf("dry-run wrote %d rows", rows)
	}
	if _, err := f.m.BackfillFlow(t.Context(), f.person, f.project, 86, true); err == nil {
		t.Fatal("changed linked PR count was accepted")
	}
	plan, err = f.m.BackfillFlow(t.Context(), f.person, f.project, 87, true)
	if err != nil || plan.Changed == nil || *plan.Changed != 174 {
		t.Fatalf("first apply: %+v, %v", plan, err)
	}
	before := flowEventCount(t, f)
	plan, err = f.m.BackfillFlow(t.Context(), f.person, f.project, 87, true)
	if err != nil || plan.Changed == nil || *plan.Changed != 0 || flowEventCount(t, f) != before {
		t.Fatalf("backfill replay: %+v, %v", plan, err)
	}
}
