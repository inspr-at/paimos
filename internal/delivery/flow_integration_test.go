// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
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
		"cut_at": cut, "prs": []int64{405}, "next_human_gate": map[string]any{"principal_id": f.person.ID, "what": "agm1 GO"},
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
	f.call(t, f.agent, "POST", path+"/rollout", release126(f, cut, nil), 200, &res)
	if res.Steps != 11 || res.Changed != 13 {
		t.Fatalf("first report: %+v", res)
	}
	before := flowEventCount(t, f)
	f.call(t, f.agent, "POST", path+"/rollout", release126(f, cut, nil), 200, &res)
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
		item.EndedAt != nil || item.Target == nil || item.Target.Minutes != 24 || item.Target.FromStep != "a" ||
		item.NextHumanGate == nil || item.NextHumanGate.What != "agm1 GO" || item.NextHumanGate.PrincipalID == nil || *item.NextHumanGate.PrincipalID != f.person.ID {
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
	raw, _ := json.Marshal(flow)
	if strings.Contains(string(raw), "sha256:") || strings.Contains(string(raw), "restart_count") {
		t.Fatalf("rollout evidence leaked into the flow: %s", raw)
	}

	// The healthy report ends the run: 100 %, no gate, no ETA.
	healthy := f.at.Add(-2 * time.Minute)
	f.call(t, f.agent, "POST", path+"/rollout", release126(f, cut, &healthy), 200, &res)
	if res.Changed != 1 {
		t.Fatalf("healthy report changed %d", res.Changed)
	}
	var run DeliveryFlowRun
	f.call(t, f.person, "GET", path+"/runs/"+res.ItemID, nil, 200, &run)
	if run.Item.PctDone != 100 || run.Item.EndedAt == nil || !run.Item.EndedAt.Equal(healthy) || run.Item.NextHumanGate != nil || len(run.Steps) != 11 || len(run.Incidents) != 1 {
		t.Fatalf("run: %+v (%d steps)", run.Item, len(run.Steps))
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
	f.call(t, f.agent, "POST", path+"/rollout", bad, 400, nil)
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
		f.call(t, f.agent, "POST", path+"/rollout", body, 200, nil)
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
	f.call(t, f.agent, "POST", path+"/rollout", release126(f, cut, nil), 200, &res)
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
	f.call(t, f.agent, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", release126(f, f.at.Add(-50*time.Minute), nil), 200, &res)
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
