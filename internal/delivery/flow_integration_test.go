// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func flowEventCount(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type IN ('delivery.step','delivery.item','delivery.incident')`).Scan(&n)
	})
	return n
}

func flowStepRow(t *testing.T, f *fixture, source, key string) (string, *time.Time, *string) {
	t.Helper()
	var stepKey string
	var ended *time.Time
	var outcome *string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT step_key,ended_at,outcome FROM delivery_flow_steps WHERE source=$1 AND source_key=$2`, source, key).Scan(&stepKey, &ended, &outcome)
	})
	return stepKey, ended, outcome
}

// release126 is a trimmed copy of the AEON-994 design run: a copy-gate repeat,
// the release path, a degraded live check with its recovery, and the human
// gate that follows.
func release126(f *fixture, cut time.Time, healthy *time.Time) map[string]any {
	step := func(key, step, kind string, actor map[string]any, start, minutes int, extra map[string]any) map[string]any {
		s := map[string]any{"key": key, "step": step, "kind": kind, "actor": actor, "started_at": cut.Add(time.Duration(start) * time.Minute)}
		if minutes >= 0 {
			s["ended_at"] = cut.Add(time.Duration(start+minutes) * time.Minute)
		}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	ops := map[string]any{"type": "agent", "principal_id": f.agent.ID, "label": "OPS", "model": nil}
	lead := map[string]any{"type": "agent", "principal_id": nil, "label": "LEAD", "model": nil}
	ci := map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}
	you := map[string]any{"type": "person", "principal_id": f.person.ID, "label": "You", "model": nil}
	steps := []any{
		step("copy-1", "copy_gate", "work", lead, 0, 4, map[string]any{"outcome": "changes"}),
		step("copy-1-fix", "copy_gate", "rework", lead, 4, 6, map[string]any{"round": 2}),
		step("copy-2", "copy_gate", "work", lead, 10, 3, map[string]any{"round": 2, "outcome": "ok"}),
		step("a", "a", "work", ci, 13, 9, map[string]any{"outcome": "green"}),
		step("b", "b", "work", ops, 22, 2, nil),
		step("k", "k", "work", ops, 24, 3, nil),
		step("l-1", "l", "work", ops, 27, 2, map[string]any{"outcome": "degraded"}),
		step("mitigation", "mitigation", "recovery", lead, 29, 10, nil),
		step("k-2", "k", "recovery", ops, 39, 2, map[string]any{"round": 2}),
		step("l-2", "l", "recovery", ops, 41, 3, map[string]any{"round": 2, "outcome": "green"}),
		step("gate", "hold", "wait", you, 44, -1, map[string]any{"wait_reason": "human_gate", "waits_for": f.person.ID}),
	}
	body := map[string]any{"schema": "aeon.rollout.v1", "direction": "forward", "release": "126", "version": "261008161926.0.0", "outcome": "in_progress",
		"cut_at": cut, "prs": []int64{405}, "next_human_gate": map[string]any{"principal_id": f.person.ID, "what": "release GO"},
		// Unrelated rollout evidence is accepted and not stored.
		"image_digest": "sha256:" + strings.Repeat("a", 64), "observation": map[string]any{"restart_count": 0},
		"steps": steps,
		"incidents": []any{map[string]any{"key": "live-1", "started_at": cut.Add(29 * time.Minute), "ended_at": cut.Add(44 * time.Minute), "severity": "degraded",
			"summary": "Live check degraded; a restart did not help", "recovery_steps": []string{"mitigation", "k-2", "l-2"}}},
	}
	if healthy != nil {
		body["outcome"], body["healthy_at"], body["live_at"] = "live", *healthy, *healthy
	}
	return body
}

func TestDeliveryFlowRolloutIngestIsIdempotentAndRead(t *testing.T) {
	// Risk: a repeated OPS report duplicates steps or events, the incident
	// loses its recovery steps, or reading reports a run as done early.
	f := newFixture(t)
	cut := f.at.Add(-50 * time.Minute)
	path := "/api/projects/" + f.project + "/delivery/flow"
	var res RolloutResult
	f.call(t, f.person, "POST", path+"/rollout", release126(f, cut, nil), 200, &res)
	if res.Steps != 11 || res.Changed != 13 {
		t.Fatalf("first report: %+v", res)
	}
	before := flowEventCount(t, f)
	f.call(t, f.person, "POST", path+"/rollout", release126(f, cut, nil), 200, &res)
	if res.Changed != 0 || flowEventCount(t, f) != before {
		t.Fatalf("replay changed %d rows, events %d → %d", res.Changed, before, flowEventCount(t, f))
	}
	var stored int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_flow_steps)+(SELECT count(*) FROM delivery_flow_items)+(SELECT count(*) FROM delivery_flow_incidents)`).Scan(&stored)
	})
	if stored != 13 {
		t.Fatalf("rows after replay: %d", stored)
	}

	var flow DeliveryFlow
	f.call(t, f.person, "GET", path+"?from="+cut.Add(-time.Minute).Format(time.RFC3339)+"&to="+f.at.Format(time.RFC3339), nil, 200, &flow)
	if len(flow.Items) != 1 || len(flow.Steps) != 11 || len(flow.Incidents) != 1 || flow.Truncated {
		t.Fatalf("flow: %d items %d steps %d incidents", len(flow.Items), len(flow.Steps), len(flow.Incidents))
	}
	item := flow.Items[0]
	if item.ID != res.ItemID || item.Kind != "release" || item.Ref != "126" || item.Title != "Release 126" || len(item.PRs) != 1 || item.PRs[0] != 405 ||
		item.EndedAt != nil || item.Target == nil || item.Target.Minutes != 61 || item.Target.FromStep != "a" ||
		item.NextHumanGate == nil || item.NextHumanGate.What != "release GO" || item.NextHumanGate.PrincipalID == nil || *item.NextHumanGate.PrincipalID != f.person.ID {
		t.Fatalf("item: %+v", item)
	}
	// a, b, k and l (healthy on round 2) of twelve release steps are done.
	if item.PctDone != 33 || item.CurrentStepID == nil || item.ETA.Basis != "none" || item.ETA.Reason == nil {
		t.Fatalf("progress %d current %v eta %+v", item.PctDone, item.CurrentStepID, item.ETA)
	}
	inc := flow.Incidents[0]
	if inc.Severity != "degraded" || len(inc.RecoveryStepIDs) != 3 || inc.RecoveryStepIDs[2] != flowStepID(f.person.TenantID, item.ID, "ops_rollout", "l-2") {
		t.Fatalf("incident: %+v", inc)
	}
	if item.QualificationEvidence != nil || item.RollbackClass != nil {
		t.Fatalf("a record without release facts invented some: %+v", item)
	}
	raw, _ := json.Marshal(flow)
	if strings.Contains(string(raw), "sha256:") || strings.Contains(string(raw), "restart_count") {
		t.Fatalf("rollout evidence leaked into the flow: %s", raw)
	}

	// The healthy report ends the run at healthy_at. The explicit gate stays
	// until a later report omits it. There is still no ETA.
	healthy := f.at.Add(-2 * time.Minute)
	f.call(t, f.person, "POST", path+"/rollout", release126(f, cut, &healthy), 200, &res)
	if res.Changed != 1 {
		t.Fatalf("healthy report changed %d", res.Changed)
	}
	var run DeliveryFlowRun
	f.call(t, f.person, "GET", path+"/runs/"+res.ItemID, nil, 200, &run)
	if run.Item.PctDone != 100 || run.Item.EndedAt == nil || !run.Item.EndedAt.Equal(healthy) || run.Item.ETA.Basis != "none" || run.Item.ETA.P50At != nil ||
		run.Item.NextHumanGate == nil || run.Item.NextHumanGate.What != "release GO" || len(run.Steps) != 11 || len(run.Incidents) != 1 {
		t.Fatalf("run: %+v (%d steps)", run.Item, len(run.Steps))
	}
	cleared := release126(f, cut, &healthy)
	delete(cleared, "next_human_gate")
	f.call(t, f.person, "POST", path+"/rollout", cleared, 200, &res)
	f.call(t, f.person, "GET", path+"/runs/"+res.ItemID, nil, 200, &run)
	if run.Item.PctDone != 100 || run.Item.EndedAt == nil || !run.Item.EndedAt.Equal(healthy) || run.Item.NextHumanGate != nil {
		t.Fatalf("omitted gate: %+v", run.Item)
	}
	// Replay at an earlier moment shows the run open again.
	f.call(t, f.person, "GET", path+"/runs/"+res.ItemID+"?at="+cut.Add(30*time.Minute).Format(time.RFC3339), nil, 200, &run)
	if run.Item.PctDone == 100 || run.Item.CurrentStepID == nil || *run.Item.CurrentStepID != flowStepID(f.person.TenantID, item.ID, "ops_rollout", "mitigation") {
		t.Fatalf("replayed moment: %+v", run.Item)
	}
	f.call(t, f.person, "GET", path+"/runs/"+flowItemID(f.person.TenantID, f.project, "release", "999"), nil, 404, nil)
	f.call(t, f.person, "GET", path+"?from="+f.at.Format(time.RFC3339)+"&to="+cut.Format(time.RFC3339), nil, 400, nil)
	bad := release126(f, cut, nil)
	bad["steps"].([]any)[0].(map[string]any)["actor"].(map[string]any)["principal_id"] = f.foreign.ID
	f.call(t, f.person, "POST", path+"/rollout", bad, 400, nil)
}

func TestDeliveryFlowETAUsesThirtyDaysOfHistory(t *testing.T) {
	// Risk: the ETA is computed from too few runs, from runs older than 30
	// days, or the history percentiles are not those of the same step.
	f := newFixture(t)
	path := "/api/projects/" + f.project + "/delivery/flow"
	report := func(release string, cut time.Time, minutes []int, open bool) {
		var steps []any
		at := cut
		for i, key := range releasePath {
			s := map[string]any{"key": key, "step": key, "kind": "work", "actor": map[string]any{"type": "agent", "principal_id": nil, "label": "OPS", "model": nil}, "started_at": at}
			if !open || i < 2 {
				at = at.Add(time.Duration(minutes[i%len(minutes)]) * time.Minute)
				s["ended_at"] = at
			}
			steps = append(steps, s)
			if open && i == 2 {
				break
			}
		}
		body := map[string]any{"schema": "aeon.rollout.v1", "release": release, "cut_at": cut, "steps": steps}
		if !open {
			body["outcome"], body["live_at"] = "live", at
		}
		f.call(t, f.person, "POST", path+"/rollout", body, 200, nil)
	}
	// One run older than 30 days never counts.
	report("old", f.at.AddDate(0, 0, -40), []int{50}, false)
	report("r1", f.at.AddDate(0, 0, -3), []int{1}, false)
	report("r2", f.at.AddDate(0, 0, -2), []int{2}, false)
	open := f.at.Add(-10 * time.Minute)
	report("now", open, []int{3}, true)
	var flow DeliveryFlow
	f.call(t, f.person, "GET", path+"?from="+open.Add(-time.Minute).Format(time.RFC3339), nil, 200, &flow)
	if len(flow.Items) != 1 || flow.Items[0].ETA.Basis != "none" || flow.Items[0].ETA.Reason == nil || !strings.Contains(*flow.Items[0].ETA.Reason, "c (2)") {
		t.Fatalf("two earlier runs: %+v", flow.Items)
	}
	report("r3", f.at.AddDate(0, 0, -1), []int{3}, false)
	f.call(t, f.person, "GET", path+"?from="+open.Add(-time.Minute).Format(time.RFC3339), nil, 200, &flow)
	eta := flow.Items[0].ETA
	// Ten steps c…l remain; c has run 4 of its usual 2 (p50) and 2.8 (p90)
	// minutes. Every other step: p50 2, p90 2.8 → 18 and 25.2 minutes.
	if eta.Basis != "history" || eta.Reason != nil || !eta.P50At.Equal(f.at.Add(18*time.Minute)) || !eta.P90At.Equal(f.at.Add(25*time.Minute+12*time.Second)) {
		t.Fatalf("eta: %+v %v %v", eta, eta.P50At, eta.P90At)
	}
	var c *FlowStep
	for i := range flow.Steps {
		if flow.Steps[i].StepKey == "c" {
			c = &flow.Steps[i]
		}
	}
	if c == nil || c.Norm.P50 == nil || *c.Norm.P50 != 2 || *c.Norm.P90 != 2.8 || c.Norm.Arion != nil {
		t.Fatalf("norm of c: %+v", c)
	}
}

func TestDeliveryFlowGitHubRunsBecomeChecksSteps(t *testing.T) {
	// Risk: a linked PR's CI runs never reach the Flow, a redelivery adds
	// events, or runs of an unlinked PR create a change from nothing.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-pr-open", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	var linked string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT coalesce(ticket_node_id::text,'') FROM delivery_items WHERE pull_request=7`).Scan(&linked)
	})
	if linked != f.ticket {
		t.Fatalf("pull 7 is not linked to the ticket: %q", linked)
	}
	head := p.Head
	created := f.at.Add(-2 * time.Hour)
	latest := githubRunBody(501, 2, defaultCIWorkflow, "pull_request", p.Branch, head, created.Add(30*time.Minute), 12*time.Minute, "success", 900)
	latest["pull_requests"] = []any{map[string]any{"number": 7}}
	first := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", p.Branch, head, created, 10*time.Minute, "failure", 900)
	first["pull_requests"] = []any{map[string]any{"number": 7}}
	other := githubRunBody(777, 1, defaultCIWorkflow, "pull_request", "work/other", strings.Repeat("9", 40), created, 5*time.Minute, "success", 901)
	other["pull_requests"] = []any{map[string]any{"number": 8}}
	g.respond = func(path string) (any, error) {
		switch path {
		case "/actions/runs?check_suite_id=900&head_sha=" + head + "&per_page=2":
			return map[string]any{"total_count": 1, "workflow_runs": []any{latest}}, nil
		case "/actions/runs/501/attempts/1":
			return first, nil
		case "/actions/runs?check_suite_id=901&head_sha=" + strings.Repeat("9", 40) + "&per_page=2":
			return map[string]any{"total_count": 1, "workflow_runs": []any{other}}, nil
		}
		return nil, errRead
	}
	suite := map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": p.Branch, "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}
	auditSend(t, f, "check_suite", "flow-suite", suite, 204)
	events := flowEventCount(t, f)
	auditSend(t, f, "check_suite", "flow-suite", suite, 204)
	if flowEventCount(t, f) != events {
		t.Fatalf("redelivery added flow events: %d → %d", events, flowEventCount(t, f))
	}
	auditSend(t, f, "check_suite", "flow-suite-unlinked", map[string]any{"action": "completed", "check_suite": map[string]any{"id": 901, "head_sha": strings.Repeat("9", 40), "head_branch": "work/other", "status": "completed", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}, 204)

	for key, want := range map[string]string{"run/501/1": "flaky", "run/501/2": "green"} {
		if stepKey, ended, outcome := flowStepRow(t, f, "github_app", key); stepKey != "ci" || ended == nil || outcome == nil || *outcome != want {
			t.Fatalf("%s: %s %v %v", key, stepKey, ended, outcome)
		}
	}
	var items, steps int
	var ref string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_flow_items),(SELECT count(*) FROM delivery_flow_steps),(SELECT ref||'/'||array_to_string(prs,',') FROM delivery_flow_items LIMIT 1)`).Scan(&items, &steps, &ref)
	})
	if items != 1 || steps != 3 || ref != "AEON-848/7" {
		t.Fatalf("items %d steps %d ref %s", items, steps, ref)
	}
}

// appendFlowSource writes a PAIMOS event the way the work queue, the review
// gate and the delivery projection do.
func appendFlowSource(t *testing.T, f *fixture, c events.Change) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.agent, c)
		return err
	})
}

func TestDeliveryFlowProjectorUsesPartialEventIndex(t *testing.T) {
	// Risk (migrations): installations without OPS's manual index scan the
	// unrelated event tail every round; predicate drift makes the index unusable.
	f := newFixture(t)
	ctx := t.Context()
	const migration = "1328_delivery_flow_event_index.sql"
	var originalOID uint32
	if err := f.d.App.QueryRow(ctx, `SELECT indexrelid FROM pg_index
		WHERE indexrelid='events_delivery_flow_idx'::regclass AND indisvalid`).Scan(&originalOID); err != nil {
		t.Fatal(err)
	}
	// Model an installation where OPS created the index before migration 1328.
	if tag, err := f.d.App.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, migration); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("remove migration record: %v (%d rows)", err, tag.RowsAffected())
	}
	if err := db.MigrateWithHook(ctx, f.d.App, nil); err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := f.d.App.QueryRow(ctx, `SELECT indexrelid FROM pg_index
		WHERE indexrelid='events_delivery_flow_idx'::regclass AND indisvalid`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if oid != originalOID {
		t.Fatal("migration rebuilt the existing valid index")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after)
			SELECT $1,$2,$3,CASE WHEN n=2049 THEN 'delivery.workxqueue.queued' ELSE 'node.updated' END,'{}'::jsonb
			FROM generate_series(1,2049) AS n`, f.person.TenantID, f.agent.ID, f.ticket)
		return err
	})
	for _, kind := range []string{"delivery.work_queue.queued", "delivery.review.queued", "delivery.state_changed"} {
		appendFlowSource(t, f, events.Change{NodeID: &f.ticket, Type: kind, After: map[string]any{}})
	}
	if _, err := f.d.Admin.Exec(ctx, `ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	// Use the production query, normal planner settings, and the projector's
	// restricted app role and read snapshot: tenant_id comes from RLS.
	err := db.InTenantReadSnapshot(db.AllProjects(ctx, "delivery flow index test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN "+flowSourceEventsSQL, int64(1024), flowSyncBatch)
		if err != nil {
			return err
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				return err
			}
			plan.WriteString(line + "\n")
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if !strings.Contains(plan.String(), "Index Scan using events_delivery_flow_idx") || !strings.Contains(plan.String(), "id >") {
			return fmt.Errorf("projector did not use the partial cursor index:\n%s", plan.String())
		}
		rows, err = tx.Query(ctx, flowSourceEventsSQL, int64(1024), flowSyncBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		var kinds []string
		for rows.Next() {
			var e flowSourceEvent
			if err := rows.Scan(&e.ID, &e.Type, &e.NodeID, &e.At, &e.Before, &e.After, &e.Metadata); err != nil {
				return err
			}
			kinds = append(kinds, e.Type)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if got := strings.Join(kinds, ","); got != "delivery.work_queue.queued,delivery.review.queued,delivery.state_changed" {
			return fmt.Errorf("projector event types: %s", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryFlowProjectsPaimosRoundsReviewsAndHolds(t *testing.T) {
	// Risk: build, fix and review rounds or holds never become steps, a
	// replayed event log duplicates them, or a hold stays open after release.
	f := newFixture(t)
	t0 := f.at.Add(-2 * time.Hour)
	at := func(m int) *time.Time { v := t0.Add(time.Duration(m) * time.Minute); return &v }
	pr := int64(7)
	build := Round{RoundInput: RoundInput{Ticket: f.ticket, Slug: "aeon-848", Kind: "first_build", Number: 1, PR: &pr}, ID: requestID(1), Project: f.project, State: "queued"}
	queue := func(m int, state string, claimant *string) {
		r := build
		r.State, r.Claimant = state, claimant
		appendFlowSource(t, f, queueChange(f.project, "transition", nil, queueSnapshot{Project: f.project, Round: &r}, *at(m)))
	}
	queue(0, "queued", nil)
	queue(2, "claimed", &f.agent.ID)
	queue(3, "running", &f.agent.ID)
	queue(30, "done", &f.agent.ID)
	review := ReviewRound{ID: requestID(2), Project: f.project, Ticket: f.ticket, Slug: "aeon-848", State: "queued"}
	reviewEvent := func(kind string, m int, state, verdict string) {
		r := review
		r.State, r.EffectiveVerdict = state, verdict
		if state != "queued" {
			r.Claimant = &f.agent.ID
		}
		appendFlowSource(t, f, events.Change{NodeID: &f.project, Type: "delivery.review." + kind, After: reviewSnapshot{Project: f.project, Round: &r}, At: at(m)})
	}
	reviewEvent("queued", 31, "queued", "")
	reviewEvent("claim", 36, "claimed", "")
	reviewEvent("verdict", 44, "completed", "changes")
	fix := build
	fix.ID, fix.Kind, fix.Number, fix.State, fix.Claimant = requestID(3), "fix", 2, "claimed", &f.agent.ID
	appendFlowSource(t, f, queueChange(f.project, "claim", nil, queueSnapshot{Project: f.project, Round: &fix}, *at(45)))
	hold := func(m int, from, to State) {
		meta, _ := json.Marshal(map[string]any{"delivery_item_id": requestID(9), "repository": "example/delivery", "pull_request": 7})
		appendFlowSource(t, f, events.Change{Type: "delivery.state_changed", NodeID: &f.ticket, Before: map[string]any{"state": from}, After: map[string]any{"state": to}, At: at(m), Metadata: meta})
	}
	hold(50, Pushed, Held)
	hold(70, Held, CIGreen)
	// A merge of a change ends it; an unrelated decision event is ignored.
	appendFlowSource(t, f, queueChange(f.project, "decision", nil, queueSnapshot{Project: f.project, Claim: &QueueClaim{Project: f.project}}, *at(71)))
	hold(80, InQueue, Merged)

	sync := func() {
		t.Helper()
		for {
			more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				return
			}
		}
	}
	sync()
	type row struct {
		key, kind, outcome string
		start, end         int
	}
	minute := func(t2 *time.Time) int {
		if t2 == nil {
			return -1
		}
		return int(t2.Sub(t0) / time.Minute)
	}
	got := map[string]row{}
	f.tx(t, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT source_key,step_key,kind,coalesce(outcome,'-'),started_at,ended_at FROM delivery_flow_steps WHERE source='paimos'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var r row
			var start time.Time
			var end *time.Time
			if err := rows.Scan(&key, &r.key, &r.kind, &r.outcome, &start, &end); err != nil {
				return err
			}
			r.start, r.end = minute(&start), minute(end)
			if strings.HasPrefix(key, "hold/") {
				key = "hold"
			}
			got[key] = row{r.key, r.kind, r.outcome, r.start, r.end}
		}
		return rows.Err()
	})
	want := map[string]row{
		"work/" + build.ID:              {"build", "work", "ok", 2, 30},
		"review/" + review.ID + "/wait": {"review", "wait", "-", 31, 36},
		"review/" + review.ID:           {"review", "work", "changes", 36, 44},
		"work/" + fix.ID:                {"build", "rework", "-", 45, -1},
		"hold":                          {"hold", "wait", "-", 50, 70},
	}
	if len(got) != len(want) {
		t.Fatalf("steps: %+v", got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("%s: %+v, want %+v", k, got[k], w)
		}
	}
	var ended *time.Time
	var prs string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ended_at,array_to_string(prs,',') FROM delivery_flow_items WHERE ref='AEON-848' AND kind='change'`).Scan(&ended, &prs)
	})
	if minute(ended) != 80 || prs != "7" {
		t.Fatalf("change ended %v prs %s", ended, prs)
	}

	// Replaying the whole log converges: no new rows, no new events.
	events := flowEventCount(t, f)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_flow_cursors SET last_event_id=0`)
		return err
	})
	sync()
	if flowEventCount(t, f) != events {
		t.Fatalf("replay added events: %d → %d", events, flowEventCount(t, f))
	}
}

func TestDeliveryFlowReadIsForProjectMembers(t *testing.T) {
	// Risk: another project's member or a nodes-only reader sees the flow,
	// or a reader can report rollouts.
	f := newFixture(t)
	cut := f.at.Add(-50 * time.Minute)
	path := "/api/projects/" + f.project + "/delivery/flow"
	var res RolloutResult
	f.call(t, f.person, "POST", path+"/rollout", release126(f, cut, nil), 200, &res)
	var otherProject, reader, nodesOnly, outsider string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'OTHER-1',id,'Other project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&otherProject); err != nil {
			return err
		}
		role := func(key string, permissions ...string) (string, error) {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,$2) RETURNING id::text`, f.person.TenantID, key).Scan(&id); err != nil {
				return "", err
			}
			for _, permission := range permissions {
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, id, permission); err != nil {
					return "", err
				}
			}
			return id, nil
		}
		readerRole, err := role("flow_reader", "delivery.read", "nodes.read")
		if err != nil {
			return err
		}
		nodesRole, err := role("flow_nodes", "nodes.read")
		if err != nil {
			return err
		}
		person := func(name string) (string, error) {
			var id string
			err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, f.person.TenantID, name).Scan(&id)
			return id, err
		}
		if reader, err = person("Flow reader"); err != nil {
			return err
		}
		if nodesOnly, err = person("Nodes only"); err != nil {
			return err
		}
		if outsider, err = person("Other project member"); err != nil {
			return err
		}
		for _, b := range []struct{ who, role, project string }{{reader, readerRole, f.project}, {nodesOnly, nodesRole, f.project}, {outsider, readerRole, otherProject}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.person.TenantID, b.who, b.role, b.project); err != nil {
				return err
			}
		}
		return nil
	})
	who := func(id string) tenant.Principal {
		return tenant.Principal{ID: id, TenantID: f.person.TenantID, Kind: tenant.Person}
	}
	window := "?from=" + cut.Format(time.RFC3339)
	var flow DeliveryFlow
	f.call(t, who(reader), "GET", path+window, nil, 200, &flow)
	if len(flow.Items) != 1 || len(flow.Steps) != 11 {
		t.Fatalf("member reads the flow: %d items %d steps", len(flow.Items), len(flow.Steps))
	}
	f.call(t, who(reader), "GET", path+"/runs/"+res.ItemID, nil, 200, nil)
	f.call(t, who(reader), "POST", path+"/rollout", release126(f, cut, nil), 403, nil)
	f.call(t, who(nodesOnly), "GET", path+window, nil, 403, nil)
	f.call(t, who(outsider), "GET", path+window, nil, 404, nil)
	f.call(t, who(outsider), "GET", path+"/runs/"+res.ItemID, nil, 404, nil)
	f.call(t, f.foreign, "GET", path+window, nil, 404, nil)
	// The other project's member asks through their own project: still nothing.
	f.call(t, who(outsider), "GET", "/api/projects/"+otherProject+"/delivery/flow/runs/"+res.ItemID, nil, 404, nil)
	// Row security keeps the rows inside the project.
	count := func(id string) int {
		var n int
		err := db.InTenant(tenant.WithPrincipal(t.Context(), who(id)), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_flow_items)+(SELECT count(*) FROM delivery_flow_steps)+(SELECT count(*) FROM delivery_flow_incidents)`).Scan(&n)
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return n
	}
	if count(reader) != 13 || count(outsider) != 0 {
		t.Fatalf("row security: member %d, other project %d", count(reader), count(outsider))
	}
}

func TestDeliveryFlowStreamSendsValueFreeLiveHints(t *testing.T) {
	// Risk: Live misses a committed step, a hint carries step content, or a
	// non-member can open the stream.
	f := newFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := f.person
		if r.Header.Get("X-Test-Foreign") != "" {
			p = f.foreign
		}
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	defer server.Close()
	url := server.URL + "/api/projects/" + f.project + "/delivery/flow/stream"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	foreign, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	foreign.Header.Set("X-Test-Foreign", "1")
	if resp, err := http.DefaultClient.Do(foreign); err != nil || resp.StatusCode != 404 {
		t.Fatalf("foreign stream: %v %v", resp, err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	defer resp.Body.Close()
	frames := make(chan [2]string, 64)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		var event string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frames <- [2]string{event, strings.TrimPrefix(line, "data: ")}
			}
		}
	}()
	next := func() [2]string {
		t.Helper()
		select {
		case fr, ok := <-frames:
			if !ok {
				t.Fatal("stream closed")
			}
			return fr
		case <-ctx.Done():
			t.Fatal("no frame before the guard timeout")
		}
		return [2]string{}
	}
	if fr := next(); fr[0] != "stream.ready" {
		t.Fatalf("first frame: %v", fr)
	}
	// The ready frame is the barrier: the stream listens before this write.
	var res RolloutResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", release126(f, f.at.Add(-50*time.Minute), nil), 200, &res)
	seen := map[string]int{}
	for i := 0; i < res.Changed; i++ {
		fr := next()
		seen[fr[0]]++
		var hint map[string]any
		if err := json.Unmarshal([]byte(fr[1]), &hint); err != nil || hint["item_id"] != res.ItemID {
			t.Fatalf("hint %v: %s", fr, fr[1])
		}
		for k := range hint {
			if k != "id" && k != "type" && k != "at" && k != "item_id" && k != "step_id" && k != "incident_id" {
				t.Fatalf("hint carries %q: %s", k, fr[1])
			}
		}
	}
	if seen["delivery.step"] != 11 || seen["delivery.incident"] != 1 || seen["delivery.item"] != 1 {
		t.Fatalf("hints: %v", seen)
	}
}

// flushGate blocks the second flush: the ready frame, then the first full
// page. The test revokes the reader and jumps the authorization clock before
// that flush returns, so the next page must not be sent.
type flushGate struct {
	http.ResponseWriter
	n       int
	blocked chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (g *flushGate) Flush() {
	g.n++
	if g.n == 2 {
		g.once.Do(func() { close(g.blocked) })
		<-g.release
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *flushGate) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func TestDeliveryFlowStreamRevokesDuringBacklog(t *testing.T) {
	// Risk: a full page of pending hints is sent without re-checking
	// delivery.read, so a revoked reader keeps receiving the project.
	f := newFixture(t)
	prev := flowStreamBatch
	flowStreamBatch = 1
	release := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	var jumped atomic.Bool
	free := func() { once.Do(func() { close(release) }) }
	var server *httptest.Server
	defer func() {
		free()
		if server != nil {
			server.Close()
		}
		flowStreamBatch = prev
		f.m.flowStreamNow = nil
	}()
	f.m.flowStreamNow = func() time.Time {
		if jumped.Load() {
			return time.Now().Add(time.Hour)
		}
		return time.Now()
	}
	step := map[string]any{"key": "a", "step": "a", "kind": "work", "actor": map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}, "started_at": f.at.Add(-time.Hour), "ended_at": f.at.Add(-50 * time.Minute)}
	var res RolloutResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", map[string]any{"schema": "aeon.rollout.v1", "release": "201", "cut_at": f.at.Add(-time.Hour), "steps": []any{step}}, 200, &res)
	if res.Changed < 2 {
		t.Fatalf("pending hints: %d", res.Changed)
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(&flushGate{ResponseWriter: w, blocked: blocked, release: release}, r.WithContext(tenant.WithPrincipal(r.Context(), f.person)))
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/projects/"+f.project+"/delivery/flow/stream?after=0", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	defer resp.Body.Close()
	frames := make(chan string, 8)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		var event string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && strings.HasPrefix(event, "delivery."):
				frames <- event
			}
		}
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("stream did not block on the full page")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	jumped.Store(true)
	free()
	got := 0
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				if got != 1 {
					t.Fatalf("delivery hints after revocation: %d", got)
				}
				return
			}
			got++
		case <-timeout.C:
			t.Fatalf("stream stayed open after revocation with %d hints", got)
		}
	}
}

func workflowRunBody(action string, id int64, attempt int, event, branch, head, workflow string, created time.Time, started *time.Time, conclusion any, pr int64) map[string]any {
	status := map[string]string{"requested": "queued", "in_progress": "in_progress", "completed": "completed"}[action]
	run := map[string]any{"id": id, "run_attempt": attempt, "name": "CI", "path": workflow, "event": event, "head_branch": branch, "head_sha": head, "status": status, "conclusion": conclusion, "created_at": created, "updated_at": created.Add(10 * time.Minute), "pull_requests": []any{}}
	if started != nil {
		run["run_started_at"] = *started
	}
	if pr > 0 {
		run["pull_requests"] = []any{map[string]any{"number": pr}}
	}
	return map[string]any{"action": action, "workflow_run": run}
}

func TestDeliveryFlowWorkflowRunShowsRunningChecks(t *testing.T) {
	// Risk: a running check never appears, an in-progress run is stored as a
	// metric fact, or a late queued delivery reopens a finished run.
	f, g := newMetricsFixture(t)
	g.respond = func(string) (any, error) { return nil, errRead }
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-live-pr", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	created := f.at.Add(-20 * time.Minute)
	auditSend(t, f, "workflow_run", "flow-live-queued", workflowRunBody("requested", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, nil, nil, 7), 204)
	if step, ended, _ := flowStepRow(t, f, "github_app", "run/501/1"); step != "ci" || ended != nil {
		t.Fatalf("queued: %s %v", step, ended)
	}
	if runs, _, _ := metricCounts(t, f); runs != 0 {
		t.Fatalf("queued run wrote %d metric runs", runs)
	}
	started := created.Add(time.Minute)
	auditSend(t, f, "workflow_run", "flow-live-running", workflowRunBody("in_progress", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, &started, nil, 7), 204)
	var start time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT started_at FROM delivery_flow_steps WHERE source='github_app' AND source_key='run/501/1'`).Scan(&start)
	})
	if !start.Equal(started) {
		t.Fatalf("running start %s", start)
	}
	var flow DeliveryFlow
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/flow?from="+created.Add(-time.Minute).Format(time.RFC3339), nil, 200, &flow)
	if len(flow.Items) != 1 || flow.Items[0].EndedAt != nil || flow.Items[0].PctDone != 0 || flow.Items[0].CurrentStepID == nil || *flow.Items[0].CurrentStepID != flowStepID(f.person.TenantID, flow.Items[0].ID, "github_app", "run/501/1") {
		t.Fatalf("live item: %+v", flow.Items)
	}
	auditSend(t, f, "workflow_run", "flow-live-done", workflowRunBody("completed", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, &started, "failure", 7), 204)
	if step, ended, outcome := flowStepRow(t, f, "github_app", "run/501/1"); step != "ci" || ended == nil || outcome == nil || *outcome != "red" {
		t.Fatalf("completed: %s %v %v", step, ended, outcome)
	}
	if runs, _, _ := metricCounts(t, f); runs != 0 {
		t.Fatalf("completed workflow run wrote %d metric runs", runs)
	}
	events := flowEventCount(t, f)
	auditSend(t, f, "workflow_run", "flow-live-done", workflowRunBody("completed", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, &started, "failure", 7), 204)
	auditSend(t, f, "workflow_run", "flow-live-late", workflowRunBody("requested", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, nil, nil, 7), 204)
	if _, ended, outcome := flowStepRow(t, f, "github_app", "run/501/1"); ended == nil || outcome == nil || *outcome != "red" || flowEventCount(t, f) != events {
		t.Fatalf("late queued reopened the run, events %d → %d", events, flowEventCount(t, f))
	}
	auditSend(t, f, "workflow_run", "flow-live-nightly", workflowRunBody("requested", 777, 1, "pull_request", p.Branch, p.Head, defaultNightlyWorkflow, created, nil, nil, 7), 204)
	auditSend(t, f, "workflow_run", "flow-live-unlinked", workflowRunBody("requested", 778, 1, "pull_request", "work/other", strings.Repeat("9", 40), defaultCIWorkflow, created, nil, nil, 8), 204)
	var steps int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_flow_steps`).Scan(&steps)
	})
	if steps != 1 || flowEventCount(t, f) != events {
		t.Fatalf("unrelated runs stored steps %d events %d", steps, flowEventCount(t, f))
	}
	auditSend(t, f, "workflow_run", "flow-live-bad", workflowRunBody("completed", 501, 1, "pull_request", p.Branch, "nope", defaultCIWorkflow, created, &started, "failure", 7), 502)
	auditSend(t, f, "workflow_run", "flow-live-action", map[string]any{"action": "deleted", "workflow_run": map[string]any{}}, 400)

	branch := "gh-readonly-queue/main/pr-7-" + p.Head
	auditSend(t, f, "workflow_run", "flow-live-queue", workflowRunBody("requested", 600, 1, "merge_group", branch, strings.Repeat("e", 40), defaultCIWorkflow, created, nil, nil, 0), 204)
	if step, ended, _ := flowStepRow(t, f, "github_app", "run/600/1/queued"); step != "queue" || ended != nil {
		t.Fatalf("queue wait: %s %v", step, ended)
	}
	auditSend(t, f, "workflow_run", "flow-live-queue-run", workflowRunBody("in_progress", 600, 1, "merge_group", branch, strings.Repeat("e", 40), defaultCIWorkflow, created, &started, nil, 0), 204)
	if _, ended, _ := flowStepRow(t, f, "github_app", "run/600/1/queued"); ended == nil || !ended.Equal(started) {
		t.Fatalf("queue wait still open: %v", ended)
	}
	if step, ended, _ := flowStepRow(t, f, "github_app", "run/600/1"); step != "queue" || ended != nil {
		t.Fatalf("queue work: %s %v", step, ended)
	}
}

func TestDeliveryFlowMergeBeforeCIStaysClosed(t *testing.T) {
	// Risk: a merge projected before the change has a flow row is discarded,
	// and the later CI ingestion leaves the merged change open forever.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-merge-open", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	p.Open, p.Merged = false, true
	f.gh.pulls[7] = p
	pr := int64(7)
	fact := MergeFact{SHA: strings.Repeat("c", 40), Head: p.Head, Title: p.Title, Branch: p.Branch, MergedBy: "github-merge-queue[bot]", PR: &pr, At: f.at}
	g.checks[fact.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "flow-merge-before-ci", auditPRBody(f, fact), 204)
	delivery := f.item(t, 7)
	if delivery.State != Merged {
		t.Fatalf("delivery state %s", delivery.State)
	}
	for {
		more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	var items int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_flow_items`).Scan(&items)
	})
	if items != 0 {
		t.Fatalf("merge of an unseen change created %d flow rows", items)
	}
	head := p.Head
	created := f.at.Add(-2 * time.Hour)
	latest := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", p.Branch, head, created, 10*time.Minute, "success", 900)
	latest["pull_requests"] = []any{map[string]any{"number": 7}}
	g.respond = func(path string) (any, error) {
		if path == "/actions/runs?check_suite_id=900&head_sha="+head+"&per_page=2" {
			return map[string]any{"total_count": 1, "workflow_runs": []any{latest}}, nil
		}
		return nil, errRead
	}
	auditSend(t, f, "check_suite", "flow-merge-suite", map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": p.Branch, "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}, 204)
	var ended *time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ended_at FROM delivery_flow_items WHERE kind='change' AND ref='AEON-848'`).Scan(&ended)
	})
	if ended == nil || !ended.Equal(delivery.Since) {
		t.Fatalf("flow ended %v, delivery merged at %s", ended, delivery.Since)
	}
}

func TestDeliveryFlowParkEpisodesStayDistinct(t *testing.T) {
	// Risk: parking the same round twice reuses one step, so the second wait
	// never opens, and a replay closes a later episode.
	f := newFixture(t)
	t0 := f.at.Add(-2 * time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	pr := int64(7)
	build := Round{RoundInput: RoundInput{Ticket: f.ticket, Slug: "aeon-848", Kind: "first_build", Number: 1, PR: &pr}, ID: requestID(1), Project: f.project, State: "queued"}
	send := func(m int, state string) {
		r := build
		r.State = state
		appendFlowSource(t, f, queueChange(f.project, "transition", nil, queueSnapshot{Project: f.project, Round: &r}, at(m)))
	}
	send(0, "queued")
	send(10, "parked")
	send(20, "queued")
	send(30, "parked")
	for {
		more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	type hold struct {
		key   string
		start time.Time
		end   *time.Time
	}
	var holds []hold
	f.tx(t, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT source_key,started_at,ended_at FROM delivery_flow_steps WHERE source='paimos' AND step_key='hold' ORDER BY started_at,source_key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h hold
			if err := rows.Scan(&h.key, &h.start, &h.end); err != nil {
				return err
			}
			holds = append(holds, h)
		}
		return rows.Err()
	})
	if len(holds) != 2 || holds[0].key == holds[1].key || !strings.Contains(holds[0].key, "/parked/") || !strings.Contains(holds[1].key, "/parked/") ||
		!holds[0].start.Equal(at(10)) || holds[0].end == nil || !holds[0].end.Equal(at(20)) || !holds[1].start.Equal(at(30)) || holds[1].end != nil {
		t.Fatalf("park episodes: %+v", holds)
	}
	events := flowEventCount(t, f)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_flow_cursors SET last_event_id=0`)
		return err
	})
	for {
		more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	var stillOpen int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_flow_steps WHERE source='paimos' AND step_key='hold' AND ended_at IS NULL`).Scan(&stillOpen)
	})
	if flowEventCount(t, f) != events || stillOpen != 1 {
		t.Fatalf("replay events %d → %d, open holds %d", events, flowEventCount(t, f), stillOpen)
	}
}

func TestDeliveryFlowStreamRevokesInsidePage(t *testing.T) {
	// Risk: revoking delivery.read while a page of several hints is flushing
	// still sends the rest of that page, because the check runs only between pages.
	f := newFixture(t)
	release := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	var jumped atomic.Bool
	free := func() { once.Do(func() { close(release) }) }
	var server *httptest.Server
	defer func() {
		free()
		if server != nil {
			server.Close()
		}
		f.m.flowStreamNow = nil
	}()
	f.m.flowStreamNow = func() time.Time {
		if jumped.Load() {
			return time.Now().Add(time.Hour)
		}
		return time.Now()
	}
	step := map[string]any{"key": "a", "step": "a", "kind": "work", "actor": map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}, "started_at": f.at.Add(-time.Hour), "ended_at": f.at.Add(-50 * time.Minute)}
	var res RolloutResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", map[string]any{"schema": "aeon.rollout.v1", "release": "201", "cut_at": f.at.Add(-time.Hour), "steps": []any{step}}, 200, &res)
	if res.Changed < 2 {
		t.Fatalf("pending hints: %d", res.Changed)
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(&flushGate{ResponseWriter: w, blocked: blocked, release: release}, r.WithContext(tenant.WithPrincipal(r.Context(), f.person)))
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/projects/"+f.project+"/delivery/flow/stream?after=0", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	defer resp.Body.Close()
	frames := make(chan string, 8)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		var event string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && strings.HasPrefix(event, "delivery."):
				frames <- event
			}
		}
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("stream did not block inside the page")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	jumped.Store(true)
	free()
	got := 0
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				if got != 1 {
					t.Fatalf("delivery hints after revocation inside the page: %d", got)
				}
				return
			}
			got++
			if got > 1 {
				t.Fatal("hint sent after revocation inside the page")
			}
		case <-timeout.C:
			t.Fatalf("stream stayed open after revocation with %d hints", got)
		}
	}
}

func TestDeliveryFlowMergeIgnoresOtherRepository(t *testing.T) {
	// Risk: a merged pull request with the same number in another repository
	// closes this change when its own delivery is still open.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-other-repo-open", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	linked := f.item(t, 7)
	if linked.State == Merged || linked.Ticket == nil || *linked.Ticket != f.ticket {
		t.Fatalf("linked delivery: %+v", linked)
	}
	mergedAt := f.at.Add(-time.Hour)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,observation,updated_at)
			VALUES($1,gen_random_uuid(),$2,$3,'other/repo',7,'work/other',$4,'merged',$5,'person','{}'::jsonb,$5)`,
			f.person.TenantID, f.project, f.ticket, strings.Repeat("c", 40), mergedAt)
		return err
	})
	head := p.Head
	created := f.at.Add(-2 * time.Hour)
	latest := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", p.Branch, head, created, 10*time.Minute, "success", 900)
	latest["pull_requests"] = []any{map[string]any{"number": 7}}
	g.respond = func(path string) (any, error) {
		if path == "/actions/runs?check_suite_id=900&head_sha="+head+"&per_page=2" {
			return map[string]any{"total_count": 1, "workflow_runs": []any{latest}}, nil
		}
		return nil, errRead
	}
	auditSend(t, f, "check_suite", "flow-other-repo-suite", map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": p.Branch, "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}, 204)
	var ended *time.Time
	var items int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), max(ended_at) FROM delivery_flow_items WHERE kind='change' AND ref='AEON-848'`).Scan(&items, &ended)
	})
	if items != 1 || ended != nil {
		t.Fatalf("other repository closed the change: items %d ended %v", items, ended)
	}
	if f.item(t, 7).State == Merged {
		t.Fatal("linked delivery was merged")
	}
}

func TestDeliveryFlowLateQueuedKeepsRunningStart(t *testing.T) {
	// Risk: a queued workflow report delivered after the run is in progress
	// replaces the running start with the run's creation time.
	f, g := newMetricsFixture(t)
	g.respond = func(string) (any, error) { return nil, errRead }
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-phase-pr", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	created := f.at.Add(-20 * time.Minute)
	started := created.Add(time.Minute)
	auditSend(t, f, "workflow_run", "flow-phase-running", workflowRunBody("in_progress", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, &started, nil, 7), 204)
	var start time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT started_at FROM delivery_flow_steps WHERE source='github_app' AND source_key='run/501/1'`).Scan(&start)
	})
	if !start.Equal(started) {
		t.Fatalf("running start %s", start)
	}
	events := flowEventCount(t, f)
	auditSend(t, f, "workflow_run", "flow-phase-late-queued", workflowRunBody("requested", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, created, nil, nil, 7), 204)
	var again time.Time
	var ended *time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT started_at, ended_at FROM delivery_flow_steps WHERE source='github_app' AND source_key='run/501/1'`).Scan(&again, &ended)
	})
	if !again.Equal(started) || ended != nil || flowEventCount(t, f) != events {
		t.Fatalf("queued redelivery moved the running step to %s ended %v, events %d → %d", again, ended, events, flowEventCount(t, f))
	}
}

func TestDeliveryFlowCompletionKeepsFlake(t *testing.T) {
	// Risk: a completion redelivery of an earlier failed attempt, after the
	// suite classified that attempt as flaky, stores it as red.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-flake-pr", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	head := p.Head
	created := f.at.Add(-2 * time.Hour)
	wall := 10 * time.Minute
	latest := githubRunBody(501, 2, defaultCIWorkflow, "pull_request", p.Branch, head, created.Add(30*time.Minute), 12*time.Minute, "success", 900)
	latest["pull_requests"] = []any{map[string]any{"number": 7}}
	first := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", p.Branch, head, created, wall, "failure", 900)
	first["pull_requests"] = []any{map[string]any{"number": 7}}
	g.respond = func(path string) (any, error) {
		switch path {
		case "/actions/runs?check_suite_id=900&head_sha=" + head + "&per_page=2":
			return map[string]any{"total_count": 1, "workflow_runs": []any{latest}}, nil
		case "/actions/runs/501/attempts/1":
			return first, nil
		}
		return nil, errRead
	}
	suite := map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": p.Branch, "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}
	auditSend(t, f, "check_suite", "flow-flake-suite", suite, 204)
	if step, ended, outcome := flowStepRow(t, f, "github_app", "run/501/1"); step != "ci" || ended == nil || outcome == nil || *outcome != "flaky" {
		t.Fatalf("suite: %s %v %v", step, ended, outcome)
	}
	events := flowEventCount(t, f)
	started := created.Add(time.Minute)
	body := workflowRunBody("completed", 501, 1, "pull_request", p.Branch, head, defaultCIWorkflow, created, &started, "failure", 7)
	body["workflow_run"].(map[string]any)["updated_at"] = started.Add(wall)
	auditSend(t, f, "workflow_run", "flow-flake-redelivery", body, 204)
	if step, ended, outcome := flowStepRow(t, f, "github_app", "run/501/1"); step != "ci" || ended == nil || outcome == nil || *outcome != "flaky" || flowEventCount(t, f) != events {
		t.Fatalf("completion redelivery: %s %v %v, events %d → %d", step, ended, outcome, events, flowEventCount(t, f))
	}
}

func TestDeliveryFlowParkReplayAtOneTimestamp(t *testing.T) {
	// Risk: parking episodes that share one timestamp are matched by time,
	// so replaying an earlier event closes the later open episode.
	f := newFixture(t)
	at := f.at.Add(-2 * time.Hour)
	pr := int64(7)
	build := Round{RoundInput: RoundInput{Ticket: f.ticket, Slug: "aeon-848", Kind: "first_build", Number: 1, PR: &pr}, ID: requestID(1), Project: f.project, State: "queued"}
	for _, state := range []string{"queued", "parked", "queued", "parked"} {
		r := build
		r.State = state
		appendFlowSource(t, f, queueChange(f.project, "transition", nil, queueSnapshot{Project: f.project, Round: &r}, at))
	}
	sync := func() {
		t.Helper()
		for {
			more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				return
			}
		}
	}
	sync()
	snapshot := func() (string, int) {
		t.Helper()
		var b strings.Builder
		var open int
		f.tx(t, func(tx pgx.Tx) error {
			rows, err := tx.Query(t.Context(), `SELECT source_key,step_key,coalesce(outcome,'-'),started_at,ended_at FROM delivery_flow_steps WHERE source='paimos' ORDER BY source_key`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var key, step, outcome string
				var start time.Time
				var end *time.Time
				if err := rows.Scan(&key, &step, &outcome, &start, &end); err != nil {
					return err
				}
				ended := "-"
				if end != nil {
					ended = end.UTC().Format(time.RFC3339Nano)
				} else if step == "hold" {
					open++
				}
				fmt.Fprintf(&b, "%s %s %s %s %s\n", key, step, outcome, start.UTC().Format(time.RFC3339Nano), ended)
			}
			return rows.Err()
		})
		return b.String(), open
	}
	before, open := snapshot()
	if open != 1 || strings.Count(before, "/parked/") != 2 {
		t.Fatalf("park episodes before replay: open %d\n%s", open, before)
	}
	events := flowEventCount(t, f)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_flow_cursors SET last_event_id=0`)
		return err
	})
	sync()
	after, open := snapshot()
	if after != before || open != 1 || flowEventCount(t, f) != events {
		t.Fatalf("replay changed rows or events %d → %d\nbefore:\n%s\nafter:\n%s", events, flowEventCount(t, f), before, after)
	}
}

// syncFlowAll projects every pending PAIMOS event of the fixture tenant.
func syncFlowAll(t *testing.T, f *fixture) {
	t.Helper()
	for {
		more, err := f.m.SyncFlow(t.Context(), f.person.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
}

// parkRound writes the queued and parked transitions of the first build round
// for the fixture ticket and pull request 7.
func parkRound(t *testing.T, f *fixture, at time.Time) {
	t.Helper()
	pr := int64(7)
	build := Round{RoundInput: RoundInput{Ticket: f.ticket, Slug: "aeon-848", Kind: "first_build", Number: 1, PR: &pr}, ID: requestID(1), Project: f.project, State: "queued"}
	for i, state := range []string{"queued", "parked"} {
		r := build
		r.State = state
		appendFlowSource(t, f, queueChange(f.project, "transition", nil, queueSnapshot{Project: f.project, Round: &r}, at.Add(time.Duration(i)*time.Minute)))
	}
}

func TestDeliveryFlowParkAfterMergeStaysClosed(t *testing.T) {
	// Risk: a merge is projected before the change has a flow row, then a PAIMOS
	// round parks the already merged work. The round names no repository, so
	// the late row skips the merge reconciliation and stays open for good,
	// with no GitHub ingestion to close it.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-park-open", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	p.Open, p.Merged = false, true
	f.gh.pulls[7] = p
	pr := int64(7)
	fact := MergeFact{SHA: strings.Repeat("c", 40), Head: p.Head, Title: p.Title, Branch: p.Branch, MergedBy: "github-merge-queue[bot]", PR: &pr, At: f.at}
	g.checks[fact.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "flow-park-merge", auditPRBody(f, fact), 204)
	delivery := f.item(t, 7)
	if delivery.State != Merged {
		t.Fatalf("delivery state %s", delivery.State)
	}
	syncFlowAll(t, f)
	var items int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_flow_items`).Scan(&items)
	})
	if items != 0 {
		t.Fatalf("merge of an unseen change created %d flow rows", items)
	}
	parkRound(t, f, f.at.Add(time.Hour))
	syncFlowAll(t, f)
	var ended *time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), max(ended_at) FROM delivery_flow_items WHERE kind='change' AND ref='AEON-848'`).Scan(&items, &ended)
	})
	if items != 1 || ended == nil || !ended.Equal(delivery.Since) {
		t.Fatalf("parked merged change: items %d ended %v, delivery merged at %s", items, ended, delivery.Since)
	}
}

func TestDeliveryFlowParkIgnoresAmbiguousRepository(t *testing.T) {
	// Risk: resolving the repository of a PAIMOS round picks a repository when
	// two claim the same pull-request number for the ticket, so another
	// repository's merged pull request closes a change that is still open.
	f, _ := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-ambiguous-open", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	if linked := f.item(t, 7); linked.State == Merged || linked.Ticket == nil || *linked.Ticket != f.ticket {
		t.Fatalf("linked delivery: %+v", linked)
	}
	mergedAt := f.at.Add(-time.Hour)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,observation,updated_at)
			VALUES($1,gen_random_uuid(),$2,$3,'other/repo',7,'work/other',$4,'merged',$5,'person','{}'::jsonb,$5)`,
			f.person.TenantID, f.project, f.ticket, strings.Repeat("c", 40), mergedAt)
		return err
	})
	parkRound(t, f, f.at.Add(time.Hour))
	syncFlowAll(t, f)
	var items int
	var ended *time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), max(ended_at) FROM delivery_flow_items WHERE kind='change' AND ref='AEON-848'`).Scan(&items, &ended)
	})
	if items != 1 || ended != nil {
		t.Fatalf("another repository closed the change: items %d ended %v", items, ended)
	}
}

func TestDeliveryFlowEarlierRunQueuedAfterLaterRunMovesItemStart(t *testing.T) {
	// Risk: runs 501 and 502 of one pull request arrive out of order. Run 502
	// is already running when run 501's queued report arrives, so the earlier
	// step is stored but the item still starts at run 502 and every window that
	// ends before it omits the item and the valid earlier step.
	f, g := newMetricsFixture(t)
	g.respond = func(string) (any, error) { return nil, errRead }
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "flow-order-pr", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)
	early := f.at.Add(-50 * time.Minute)
	late := f.at.Add(-20 * time.Minute)
	lateStart := late.Add(time.Minute)
	itemStart := func() time.Time {
		var at time.Time
		f.tx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT started_at FROM delivery_flow_items WHERE kind='change' AND ref='AEON-848'`).Scan(&at)
		})
		return at
	}
	auditSend(t, f, "workflow_run", "flow-order-later", workflowRunBody("in_progress", 502, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, late, &lateStart, nil, 7), 204)
	if got := itemStart(); !got.Equal(lateStart) {
		t.Fatalf("item start after the later run: %s", got)
	}
	auditSend(t, f, "workflow_run", "flow-order-earlier", workflowRunBody("requested", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, early, nil, nil, 7), 204)
	if got := itemStart(); !got.Equal(early) {
		t.Fatalf("item start %s after the earlier run's queued report, want %s", got, early)
	}
	var flow DeliveryFlow
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/flow?from="+early.Add(-time.Minute).Format(time.RFC3339)+"&to="+early.Add(10*time.Minute).Format(time.RFC3339), nil, 200, &flow)
	if len(flow.Items) != 1 || len(flow.Steps) != 1 || flow.Steps[0].ID != flowStepID(f.person.TenantID, flow.Items[0].ID, "github_app", "run/501/1") {
		t.Fatalf("window before the later run: %d items, steps %+v", len(flow.Items), flow.Steps)
	}
	// The same queued report again changes nothing: the step exists.
	events := flowEventCount(t, f)
	auditSend(t, f, "workflow_run", "flow-order-earlier-again", workflowRunBody("requested", 501, 1, "pull_request", p.Branch, p.Head, defaultCIWorkflow, early, nil, nil, 7), 204)
	if got := itemStart(); !got.Equal(early) || flowEventCount(t, f) != events {
		t.Fatalf("queued redelivery: item start %s, events %d → %d", got, events, flowEventCount(t, f))
	}
}

// release127 is the record the OPS producer emits from release 127 on
// (AEON-1022): the 126 path a → l with the exact-SHA rehearsal and the full test
// catalogue inside the merge-group run, a human wait for the qualification, the
// qualification evidence reference and the rollback class. Minutes after the cut:
// a 0–16, rehearsal 0–11 (alongside), catalogue 1–23 (the pole), b 23–24, c 24–36,
// d 36–37, e 37–47 then the person's wait 47–52, f 52–54, g 54–55, h 55–60,
// i 60–61, j 61–64, k 64–70, l 70–75. through names the last step in the record.
func release127(f *fixture, cut time.Time, through string) map[string]any {
	ops := map[string]any{"type": "agent", "principal_id": f.agent.ID, "label": "OPS", "model": nil}
	ci := map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}
	review := map[string]any{"type": "agent", "principal_id": nil, "label": "Reviewer", "model": nil}
	you := map[string]any{"type": "person", "principal_id": f.person.ID, "label": "You", "model": nil}
	type row struct {
		key, step, kind string
		actor           map[string]any
		start, end      int
		extra           map[string]any
	}
	rows := []row{
		{"a", "a", "work", ci, 0, 16, map[string]any{"outcome": "green"}},
		{"rehearsal", "rehearsal", "work", ci, 0, 11, map[string]any{"outcome": "green", "side": true}},
		{"catalogue", "catalogue", "work", ci, 1, 23, map[string]any{"outcome": "green"}},
		{"b", "b", "work", ops, 23, 24, nil},
		{"c", "c", "work", ci, 24, 36, nil},
		{"d", "d", "work", ops, 36, 37, nil},
		{"e", "e", "work", ops, 37, 47, nil},
		{"e-approval", "e", "wait", you, 47, 52, map[string]any{"wait_reason": "human_gate", "waits_for": f.person.ID}},
		{"f", "f", "work", ci, 52, 54, nil},
		{"g", "g", "work", ops, 54, 55, nil},
		{"h", "h", "work", review, 55, 60, map[string]any{"outcome": "ok"}},
		{"i", "i", "work", ci, 60, 61, nil},
		{"j", "j", "work", ops, 61, 64, nil},
		{"k", "k", "work", ops, 64, 70, nil},
		{"l", "l", "work", ops, 70, 75, map[string]any{"outcome": "green"}},
	}
	var steps []any
	for _, r := range rows {
		s := map[string]any{"key": r.key, "step": r.step, "kind": r.kind, "actor": r.actor, "started_at": cut.Add(time.Duration(r.start) * time.Minute), "ended_at": cut.Add(time.Duration(r.end) * time.Minute)}
		for k, v := range r.extra {
			s[k] = v
		}
		steps = append(steps, s)
		if r.key == through {
			break
		}
	}
	return map[string]any{"schema": "aeon.rollout.v1", "direction": "forward", "release": "127", "version": "261009063244.0.0", "outcome": "in_progress",
		"cut_at": cut, "prs": []int64{426},
		// The qualification object is the one scripts/verify-live.mjs validates; the Flow keeps only its evidence reference.
		"qualification": map[string]any{"version": "261009063244.0.0", "asset": "paimos-agentd-darwin-arm64", "sha256": strings.Repeat("b", 64), "sha256sums": strings.Repeat("c", 64),
			"operator": "markus-barta", "spctl": true, "foreground_socket": true, "acl_fixture": true, "attach_preview": true, "touch_id": true, "evidence": "AEON-487/comment/native-qualification"},
		"rollback_class": "digest_safe", "image_digest": "sha256:" + strings.Repeat("a", 64),
		"steps": steps}
}

func TestDeliveryFlowRolloutRecordsCatalogueRehearsalQualificationAndRollbackClass(t *testing.T) {
	// Risk: the Flow drops the catalogue or the rehearsal step (or refuses the
	// whole record for them), loses the qualification evidence or the rollback
	// class, stores more of the qualification object than its reference, counts
	// the catalogue as a phase of its own, or a partial re-report erases a fact.
	f := newFixture(t)
	cut := f.at.Add(-90 * time.Minute)
	path := "/api/projects/" + f.project + "/delivery/flow"
	var res RolloutResult
	f.call(t, f.person, "POST", path+"/rollout", release127(f, cut, "e-approval"), 200, &res)
	if res.Steps != 8 || res.Changed != 9 {
		t.Fatalf("first report: %+v", res)
	}
	item := res.ItemID
	var run DeliveryFlowRun
	f.call(t, f.person, "GET", path+"/runs/"+item, nil, 200, &run)
	steps := map[string]FlowStep{}
	for _, s := range run.Steps {
		steps[s.StepKey+"/"+s.Kind] = s
	}
	cat, ok := steps["catalogue/work"]
	if !ok || cat.EndedAt == nil || cat.EndedAt.Sub(cat.StartedAt) != 22*time.Minute || cat.Outcome == nil || *cat.Outcome != "green" || cat.Side || cat.Actor.Type != "ci" || cat.Source != "ops_rollout" {
		t.Fatalf("catalogue: %+v", cat)
	}
	reh, ok := steps["rehearsal/work"]
	if !ok || reh.EndedAt == nil || reh.EndedAt.Sub(reh.StartedAt) != 11*time.Minute || !reh.Side {
		t.Fatalf("rehearsal: %+v", reh)
	}
	if run.Item.QualificationEvidence == nil || *run.Item.QualificationEvidence != "AEON-487/comment/native-qualification" || run.Item.RollbackClass == nil || *run.Item.RollbackClass != "digest_safe" {
		t.Fatalf("release facts: %+v", run.Item)
	}
	// Progress follows a → l. Done: a, b, c, d and e's work (the person's wait
	// is not a phase of its own); the rehearsal and the catalogue add none.
	if run.Item.PctDone != 42 {
		t.Fatalf("progress %d", run.Item.PctDone)
	}
	raw, _ := json.Marshal(run)
	for _, leaked := range []string{"paimos-agentd-darwin-arm64", strings.Repeat("b", 64), strings.Repeat("c", 64), "touch_id", "markus-barta", "sha256:"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("the qualification object leaked %q into the flow", leaked)
		}
	}

	// The list read carries the same facts.
	var flow DeliveryFlow
	f.call(t, f.person, "GET", path+"?from="+cut.Add(-time.Minute).Format(time.RFC3339)+"&to="+f.at.Format(time.RFC3339), nil, 200, &flow)
	if len(flow.Items) != 1 || flow.Items[0].RollbackClass == nil || *flow.Items[0].RollbackClass != "digest_safe" || flow.Items[0].QualificationEvidence == nil {
		t.Fatalf("flow items: %+v", flow.Items)
	}

	// A repeated report changes nothing and emits no event.
	before := flowEventCount(t, f)
	f.call(t, f.person, "POST", path+"/rollout", release127(f, cut, "e-approval"), 200, &res)
	if res.Changed != 0 || flowEventCount(t, f) != before {
		t.Fatalf("replay changed %d rows, events %d → %d", res.Changed, before, flowEventCount(t, f))
	}

	// A report without the release facts keeps what is recorded: evidence is
	// never erased by a partial record.
	partial := release127(f, cut, "e-approval")
	delete(partial, "qualification")
	delete(partial, "rollback_class")
	f.call(t, f.person, "POST", path+"/rollout", partial, 200, &res)
	f.call(t, f.person, "GET", path+"/runs/"+item, nil, 200, &run)
	if res.Changed != 0 || run.Item.QualificationEvidence == nil || run.Item.RollbackClass == nil || *run.Item.RollbackClass != "digest_safe" {
		t.Fatalf("partial report: %+v changed %d", run.Item, res.Changed)
	}
	// A qualification object without an evidence reference is accepted and says nothing.
	partial["qualification"] = map[string]any{"version": "261009063244.0.0", "operator": "markus-barta"}
	f.call(t, f.person, "POST", path+"/rollout", partial, 200, &res)
	f.call(t, f.person, "GET", path+"/runs/"+item, nil, 200, &run)
	if res.Changed != 0 || run.Item.QualificationEvidence == nil || *run.Item.QualificationEvidence != "AEON-487/comment/native-qualification" {
		t.Fatalf("evidence-less qualification: %+v", run.Item)
	}

	// A corrected record replaces both facts, and the finished record ends the run.
	healthy := f.at.Add(-10 * time.Minute)
	full := release127(f, cut, "")
	full["outcome"], full["live_at"], full["healthy_at"] = "live", healthy, healthy
	full["rollback_class"] = "restore_required"
	full["qualification"].(map[string]any)["evidence"] = "AEON-487/comment/native-qualification-2"
	f.call(t, f.person, "POST", path+"/rollout", full, 200, &res)
	f.call(t, f.person, "GET", path+"/runs/"+item, nil, 200, &run)
	if run.Item.PctDone != 100 || run.Item.EndedAt == nil || *run.Item.RollbackClass != "restore_required" || *run.Item.QualificationEvidence != "AEON-487/comment/native-qualification-2" || len(run.Steps) != 15 {
		t.Fatalf("corrected record: %+v (%d steps)", run.Item, len(run.Steps))
	}
}

func TestDeliveryFlowOpenCatalogueIsTheCurrentStepNotAPhase(t *testing.T) {
	// Risk: a catalogue that is still running counts as progress, or does not
	// show as the step the release is in when it is the pole (Arion v5 §4b).
	f := newFixture(t)
	cut := f.at.Add(-20 * time.Minute)
	path := "/api/projects/" + f.project + "/delivery/flow"
	ci := map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}
	body := map[string]any{"schema": "aeon.rollout.v1", "release": "128", "cut_at": cut, "steps": []any{
		map[string]any{"key": "a", "step": "a", "kind": "work", "actor": ci, "started_at": cut, "ended_at": cut.Add(16 * time.Minute), "outcome": "green"},
		map[string]any{"key": "catalogue", "step": "catalogue", "kind": "work", "actor": ci, "started_at": cut.Add(time.Minute)},
	}}
	var res RolloutResult
	f.call(t, f.person, "POST", path+"/rollout", body, 200, &res)
	var run DeliveryFlowRun
	f.call(t, f.person, "GET", path+"/runs/"+res.ItemID, nil, 200, &run)
	if run.Item.PctDone != 8 || run.Item.CurrentStepID == nil || *run.Item.CurrentStepID != flowStepID(f.person.TenantID, res.ItemID, "ops_rollout", "catalogue") {
		t.Fatalf("progress %d current %v", run.Item.PctDone, run.Item.CurrentStepID)
	}
	if run.Item.QualificationEvidence != nil || run.Item.RollbackClass != nil {
		t.Fatalf("facts nobody reported were filled in: %+v", run.Item)
	}
}

func TestDeliveryFlowStepKeyCheckKeepsGuardingAfterWidening(t *testing.T) {
	// Risk: widening the step_key check (1305) opens the column to any text, or
	// drops a key a previous binary writes. The API refuses unknown keys first,
	// so this asks the database directly.
	f := newFixture(t)
	path := "/api/projects/" + f.project + "/delivery/flow"
	var res RolloutResult
	f.call(t, f.person, "POST", path+"/rollout", release126(f, f.at.Add(-50*time.Minute), nil), 200, &res)
	update := func(key string) error {
		_, err := f.d.Admin.Exec(t.Context(), `UPDATE delivery_flow_steps SET step_key=$1 WHERE source_key='b'`, key)
		return err
	}
	for _, key := range []string{"catalogue", "rehearsal", "b", "live_check"} {
		if err := update(key); err != nil {
			t.Fatalf("key %s refused: %v", key, err)
		}
	}
	var pgErr *pgconn.PgError
	if err := update("catalog"); !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "delivery_flow_steps_step_key_check" {
		t.Fatalf("an unknown key must violate the step_key check: %v", err)
	}
}

type flowScanTraceKey struct{}

// flowScanBarrier pauses after the source rows are read, while their transaction
// is still open. Separate pools give each pass its own deterministic barrier.
type flowScanBarrier struct {
	paused      chan struct{}
	resume      chan struct{}
	tenantLocks atomic.Int32
	rows        atomic.Int64
}

func (b *flowScanBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == db.TenantFenceSQL {
		b.tenantLocks.Add(1)
	}
	if strings.Contains(data.SQL, "FROM events") && strings.Contains(data.SQL, `delivery.work\_queue.%`) {
		return context.WithValue(ctx, flowScanTraceKey{}, true)
	}
	return ctx
}

func (b *flowScanBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(flowScanTraceKey{}) != true {
		return
	}
	b.rows.Store(data.CommandTag.RowsAffected())
	select {
	case b.paused <- struct{}{}:
	case <-ctx.Done():
		return
	}
	select {
	case <-b.resume:
	case <-ctx.Done():
	}
}

func flowBarrierModule(t *testing.T, f *fixture) (*Module, *flowScanBarrier) {
	t.Helper()
	b := &flowScanBarrier{paused: make(chan struct{}, 1), resume: make(chan struct{})}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = b
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m := New(pool, f.m.config, nil, f.gh, nil)
	m.now = f.m.now
	return m, b
}

func waitFlowBarrier(t *testing.T, ctx context.Context, reached <-chan struct{}) {
	t.Helper()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatalf("flow barrier not reached: %v", ctx.Err())
	}
}

func startFlowPass(ctx context.Context, m *Module, tid string) <-chan error {
	done := make(chan error, 1)
	go func() {
		more, err := m.SyncFlow(ctx, tid)
		if err == nil && more {
			err = errors.New("small flow batch unexpectedly reports more events")
		}
		done <- err
	}()
	return done
}

func waitFlowPass(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("flow pass did not finish: %v", ctx.Err())
	}
}

func TestDeliveryFlowEmptyPassDoesNotLockTenant(t *testing.T) {
	// Risk: an idle projector stalls all tenant writers while scanning an
	// unrelated event tail, or takes the tenant lock after that scan.
	f := newFixture(t)
	appendFlowSource(t, f, events.Change{NodeID: &f.ticket, Type: "node.updated", After: map[string]any{"title": "Unrelated event"}})
	m, scan := flowBarrierModule(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pass := startFlowPass(ctx, m, f.person.TenantID)
	waitFlowBarrier(t, ctx, scan.paused)
	acquired, release := make(chan struct{}), make(chan struct{})
	writer := make(chan error, 1)
	go func() {
		writer <- db.InTenant(db.AllProjects(ctx, "concurrent flow test writer"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
				return err
			}
			close(acquired)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	waitFlowBarrier(t, ctx, acquired)
	close(scan.resume)
	// Keep the writer's lock until the entire empty pass has returned.
	waitFlowPass(t, ctx, pass)
	close(release)
	waitFlowPass(t, ctx, writer)
	if scan.rows.Load() != 0 || scan.tenantLocks.Load() != 0 {
		t.Fatalf("empty pass read %d matching events and took %d tenant locks", scan.rows.Load(), scan.tenantLocks.Load())
	}
	var cursors int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_flow_cursors`).Scan(&cursors)
	})
	if cursors != 0 || flowEventCount(t, f) != 0 {
		t.Fatalf("empty pass wrote %d cursors or flow events", cursors)
	}
}

func TestDeliveryFlowConcurrentPassesApplyEachEventOnce(t *testing.T) {
	// Risk: passes that read the same cursor duplicate intermediate actor
	// changes, regress the cursor, or drop the unapplied tail of a newer batch.
	for _, partial := range []bool{false, true} {
		name := "same_batch"
		if partial {
			name = "overlapping_batch"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			round := Round{RoundInput: RoundInput{Ticket: f.ticket, Slug: "aeon-848", Kind: "first_build", Number: 1}, ID: requestID(1), Project: f.project}
			appendRound := func(state, claimant string, at time.Time) {
				r := round
				r.State, r.Claimant = state, &claimant
				appendFlowSource(t, f, queueChange(f.project, "transition", nil, queueSnapshot{Project: f.project, Round: &r}, at))
			}
			appendRound("claimed", f.agent.ID, f.at)
			appendRound("running", f.person.ID, f.at.Add(time.Minute))
			first, firstScan := flowBarrierModule(t, f)
			firstPass := startFlowPass(ctx, first, f.person.TenantID)
			waitFlowBarrier(t, ctx, firstScan.paused)
			if partial {
				appendRound("done", f.person.ID, f.at.Add(2*time.Minute))
			}
			second, secondScan := flowBarrierModule(t, f)
			secondPass := startFlowPass(ctx, second, f.person.TenantID)
			waitFlowBarrier(t, ctx, secondScan.paused)
			want := int64(2)
			if partial {
				want = 3
			}
			if firstScan.rows.Load() != 2 || secondScan.rows.Load() != want {
				t.Fatalf("passes did not read overlapping source events: %d, %d", firstScan.rows.Load(), secondScan.rows.Load())
			}
			close(firstScan.resume)
			waitFlowPass(t, ctx, firstPass)
			close(secondScan.resume)
			waitFlowPass(t, ctx, secondPass)
			var changes, steps int64
			var cursor, last int64
			var actor string
			var ended *time.Time
			f.tx(t, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.step'`).Scan(&changes); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_flow_steps`).Scan(&steps); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT actor_principal_id::text,ended_at FROM delivery_flow_steps WHERE source_key=$1`, "work/"+round.ID).Scan(&actor, &ended); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT last_event_id,(SELECT max(id) FROM events WHERE type LIKE 'delivery.work\_queue.%') FROM delivery_flow_cursors`).Scan(&cursor, &last)
			})
			if changes != want || steps != 1 || actor != f.person.ID || cursor != last {
				t.Fatalf("changes %d want %d, steps %d, actor %s, cursor %d last source %d", changes, want, steps, actor, cursor, last)
			}
			if partial && (ended == nil || !ended.Equal(f.at.Add(2*time.Minute))) || !partial && ended != nil {
				t.Fatalf("unapplied tail completion: %v", ended)
			}
		})
	}
}
