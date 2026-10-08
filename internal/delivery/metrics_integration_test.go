// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// metricsFake serves recorded GitHub API bodies by request path.
type metricsFake struct {
	*auditFake
	respond func(path string) (any, error)
	calls   []string
}

func (g *metricsFake) MetricsRead(_ context.Context, read func(func(string, any) error) error) error {
	return read(func(path string, out any) error {
		g.calls = append(g.calls, path)
		body, err := g.respond(path)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(body)
		return json.Unmarshal(raw, out)
	})
}

func newMetricsFixture(t *testing.T) (*fixture, *metricsFake) {
	t.Helper()
	f := newFixture(t)
	g := &metricsFake{auditFake: &auditFake{fakeGitHub: f.gh, facts: map[string]*MergeFact{}, checks: map[string][]Check{}}}
	g.respond = func(path string) (any, error) { return nil, fmt.Errorf("unexpected GitHub read %s", path) }
	f.m.github = g
	var source MetricSource
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery/metrics/source", map[string]any{"repository": f.m.config.Repository}, 200, &source)
	if !source.AppConnected || source.Backfill != "not_started" || source.CIWorkflow != defaultCIWorkflow {
		t.Fatalf("source: %+v", source)
	}
	return f, g
}

func githubRunBody(id int64, attempt int, workflow, event, branch, head string, created time.Time, wall time.Duration, conclusion string, suite int64) map[string]any {
	return map[string]any{"id": id, "run_attempt": attempt, "name": "CI", "path": workflow, "event": event, "head_branch": branch, "head_sha": head, "status": "completed", "conclusion": conclusion,
		"created_at": created, "run_started_at": created.Add(time.Minute), "updated_at": created.Add(time.Minute + wall), "check_suite_id": suite, "pull_requests": []any{}}
}

func metricCounts(t *testing.T, f *fixture) (runs, pulls, marks int) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_metric_runs),(SELECT count(*) FROM delivery_metric_pulls),(SELECT count(*) FROM delivery_metric_marks)`).Scan(&runs, &pulls, &marks)
	})
	return
}

func TestDeliveryMetricsWebhookRecordsEachEventKind(t *testing.T) {
	// Risk: a webhook kind is accepted but its timing fact is lost, a
	// redelivery duplicates it, or a failed GitHub read reports success.
	f, g := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	opened := f.at.Add(-3 * time.Hour)
	auditSend(t, f, "pull_request", "metrics-pr-open", map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "created_at": opened, "head": map[string]any{"sha": p.Head, "ref": p.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}}, 204)

	head := strings.Repeat("d", 40)
	created := f.at.Add(-2 * time.Hour)
	suite := map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": "work/aeon-848-delivery", "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}
	latest := githubRunBody(501, 2, defaultCIWorkflow, "pull_request", "work/aeon-848-delivery", head, created.Add(30*time.Minute), 12*time.Minute, "success", 900)
	latest["pull_requests"] = []any{map[string]any{"number": 7}}
	first := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", "work/aeon-848-delivery", head, created, 10*time.Minute, "failure", 900)
	g.respond = func(path string) (any, error) {
		switch path {
		case "/actions/runs?check_suite_id=900&head_sha=" + head + "&per_page=2":
			return map[string]any{"total_count": 1, "workflow_runs": []any{latest}}, nil
		case "/actions/runs/501/attempts/1":
			return first, nil
		}
		return nil, errRead
	}
	auditSend(t, f, "check_suite", "metrics-suite", suite, 204)
	// A redelivery converges on the same rows.
	auditSend(t, f, "check_suite", "metrics-suite", suite, 204)
	pending := f.at.Add(-90 * time.Minute)
	auditSend(t, f, "status", "metrics-review-pending", map[string]any{"sha": head, "state": "pending", "context": "aeon/review", "created_at": pending}, 204)
	auditSend(t, f, "status", "metrics-review-ok", map[string]any{"sha": head, "state": "success", "context": "aeon/review", "created_at": pending.Add(11 * time.Minute)}, 204)
	// Other contexts and check runs carry no metric fact of their own.
	auditSend(t, f, "status", "metrics-other-status", map[string]any{"sha": head, "state": "success", "context": "lint", "created_at": pending}, 204)
	auditSend(t, f, "check_run", "metrics-check-run", map[string]any{"action": "completed", "check_run": map[string]any{"head_sha": head, "name": "go-test", "status": "completed", "conclusion": "success"}}, 204)

	runs, pulls, marks := metricCounts(t, f)
	if runs != 2 || pulls != 1 || marks != 2 {
		t.Fatalf("facts runs=%d pulls=%d marks=%d, want 2/1/2", runs, pulls, marks)
	}
	var conclusions string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT string_agg(attempt||':'||conclusion||':'||coalesce(pull_request::text,'-')||':'||source,',' ORDER BY attempt) FROM delivery_metric_runs WHERE run_id=501`).Scan(&conclusions)
	})
	if conclusions != "1:failure:-:webhook,2:success:7:webhook" {
		t.Fatalf("run attempts: %s", conclusions)
	}

	// A merged close records the merge time on the existing pull.
	merged := f.at.Add(-time.Hour)
	body := auditPRBody(f, MergeFact{SHA: strings.Repeat("e", 40), Head: p.Head, Title: "AEON-848: delivery", Branch: p.Branch, MergedBy: "octo", PR: ptrInt(7), At: merged})
	body["pull_request"].(map[string]any)["created_at"] = opened
	auditSend(t, f, "pull_request", "metrics-pr-merged", body, 204)

	// A suite whose run GitHub cannot return stays retryable: 502, no row.
	g.respond = func(string) (any, error) { return nil, errRead }
	broken := map[string]any{"action": "completed", "check_suite": map[string]any{"id": 901, "head_sha": strings.Repeat("f", 40), "head_branch": "work/x", "status": "completed", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}
	auditSend(t, f, "check_suite", "metrics-suite-broken", broken, 502)
	if runs, _, _ := metricCounts(t, f); runs != 2 {
		t.Fatalf("failed read stored runs: %d", runs)
	}

	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	if out.Source == nil || out.Source.CoveredSince == nil || !out.Source.CoveredSince.Equal(f.at) || len(out.Metrics) != 12 {
		t.Fatalf("metrics source: %+v (%d metrics)", out.Source, len(out.Metrics))
	}
	wall := metricByKey(t, out.Metrics, "pr_ci_wall")
	wantWindow(t, wall, 30, "partial", 1, f64(10), f64(10), f64(10))
	wantWindow(t, metricByKey(t, out.Metrics, "flaked_failures"), 30, "partial", 1, f64(100), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "pr_open_to_merged"), 30, "partial", 1, f64(120), f64(120), f64(120))
	wantWindow(t, metricByKey(t, out.Metrics, "review_time"), 30, "partial", 1, f64(11), f64(11), f64(11))
}

func ptrInt(n int64) *int64 { return &n }

func TestDeliveryMetricsBackfillIsBoundedResumableAndIdempotent(t *testing.T) {
	// Risk: a backfill that fails midway loses its place or writes half a
	// step, a repeat duplicates facts, or one call reads without bound.
	f, g := newMetricsFixture(t)
	day := f.at.AddDate(0, 0, -3) // 2026-10-04
	ci := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	nightly := "/actions/workflows/nightly-full.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	rerun := githubRunBody(601, 2, defaultCIWorkflow, "pull_request", "work/a", strings.Repeat("1", 40), day.Add(2*time.Hour), 9*time.Minute, "success", 0)
	failOn := ""
	g.respond = func(path string) (any, error) {
		if path == failOn {
			return nil, errRead
		}
		switch {
		case path == ci:
			return map[string]any{"total_count": 2, "workflow_runs": []any{
				githubRunBody(600, 1, defaultCIWorkflow, "merge_group", "gh-readonly-queue/main/pr-7-"+strings.Repeat("a", 40), strings.Repeat("a", 40), day.Add(time.Hour), 14*time.Minute, "success", 0), rerun}}, nil
		case path == "/actions/runs/601/attempts/1":
			return githubRunBody(601, 1, defaultCIWorkflow, "pull_request", "work/a", strings.Repeat("1", 40), day.Add(time.Hour), 8*time.Minute, "failure", 0), nil
		case path == nightly:
			return map[string]any{"total_count": 1, "workflow_runs": []any{githubRunBody(602, 1, defaultNightlyWorkflow, "schedule", "main", strings.Repeat("2", 40), day.Add(3*time.Hour), 40*time.Minute, "failure", 0)}}, nil
		case strings.HasPrefix(path, "/actions/workflows/"):
			return map[string]any{"total_count": 0, "workflow_runs": []any{}}, nil
		case path == "/pulls?state=all&sort=updated&direction=desc&per_page=100&page=1":
			pull := func(n int64, updated time.Time) map[string]any {
				return map[string]any{"number": n, "created_at": updated.Add(-time.Hour), "updated_at": updated, "merged_at": updated, "closed_at": updated, "head": map[string]any{"ref": "work/x"}, "base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": f.m.config.Repository}}}
			}
			return []any{pull(7, day.Add(5*time.Hour)), pull(8, day.Add(-60*24*time.Hour))}, nil
		}
		return nil, fmt.Errorf("unexpected GitHub read %s", path)
	}
	// 31 days × 2 workflows need two bounded steps.
	var step BackfillResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{"days": 30}, 200, &step)
	if step.State != "running" || step.Calls > backfillCallBudget || step.Phase != "runs" || step.Day == nil {
		t.Fatalf("first step: %+v", step)
	}
	firstStep := step
	// The next step fails on the pulls page: nothing is written and the
	// stored position stays where the first step left it.
	failOn = "/pulls?state=all&sort=updated&direction=desc&per_page=100&page=1"
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 502, nil)
	runsBefore, pullsBefore, _ := metricCounts(t, f)
	var cursor []byte
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT backfill_cursor FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&cursor)
	})
	var stored backfillCursor
	if err := json.Unmarshal(cursor, &stored); err != nil || stored.Phase != "runs" || stored.Day != *firstStep.Day {
		t.Fatalf("failed step moved the cursor: %s (%v)", cursor, err)
	}
	if pullsBefore != 0 {
		t.Fatalf("failed step wrote %d pulls", pullsBefore)
	}
	failOn = ""
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 200, &step)
	if step.State != "done" || step.Phase != "done" {
		t.Fatalf("resumed step: %+v", step)
	}
	runs, pulls, _ := metricCounts(t, f)
	if runs != 4 || pulls != 1 || runsBefore > runs {
		t.Fatalf("backfilled runs=%d pulls=%d (before %d)", runs, pulls, runsBefore)
	}
	// Done is sticky without restart and reads nothing.
	calls := len(g.calls)
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 200, &step)
	if step.State != "done" || len(g.calls) != calls {
		t.Fatalf("done backfill read GitHub again: %+v", step)
	}
	// A restart reads the same history and converges on the same rows.
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{"days": 30, "restart": true}, 200, &step)
	for step.State != "done" {
		f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 200, &step)
	}
	if r, p, _ := metricCounts(t, f); r != runs || p != pulls {
		t.Fatalf("restart duplicated facts: runs %d→%d pulls %d→%d", runs, r, pulls, p)
	}
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	if out.Source == nil || out.Source.Backfill != "done" || out.Source.CoveredSince == nil || !out.Source.CoveredSince.Equal(dayStart(f.at.AddDate(0, 0, -30))) {
		t.Fatalf("source after backfill: %+v", out.Source)
	}
	wantWindow(t, metricByKey(t, out.Metrics, "first_attempt_green"), 30, "ok", 1, f64(0), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "queue_run_wall"), 30, "ok", 1, f64(14), f64(14), f64(14))
	wantWindow(t, metricByKey(t, out.Metrics, "nightly_green"), 30, "ok", 1, f64(0), nil, nil)
}

func TestDeliveryMetricsBackfillStepsCoverEveryDayOnce(t *testing.T) {
	// Risk: resuming from a stored cursor skips or repeats a day.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	since := dayStart(now.AddDate(0, 0, -60))
	seen := map[string]int{}
	get := func(path string, out any) error {
		if strings.HasPrefix(path, "/pulls") {
			return json.Unmarshal([]byte(`[]`), out)
		}
		seen[path]++
		return json.Unmarshal([]byte(`{"total_count":0,"workflow_runs":[]}`), out)
	}
	cursor := backfillCursor{Since: since, Phase: "runs", Day: since.Format("2006-01-02"), Page: 1}
	steps := 0
	for cursor.Phase != "done" {
		next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow, defaultNightlyWorkflow}, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Calls > backfillCallBudget {
			t.Fatalf("step used %d calls", batch.Calls)
		}
		cursor = next
		if steps++; steps > 10 {
			t.Fatal("backfill did not finish")
		}
	}
	if len(seen) != 2*61 || steps < 3 {
		t.Fatalf("listed %d workflow days in %d steps, want %d", len(seen), steps, 2*61)
	}
	for path, n := range seen {
		if n != 1 {
			t.Fatalf("%s read %d times", path, n)
		}
	}
}

func TestDeliveryMetricsReadIsProjectMembersOnly(t *testing.T) {
	// Risk: delivery numbers or facts leak to people outside the project.
	f, _ := newMetricsFixture(t)
	release := map[string]any{"facts": []any{map[string]any{"kind": "release", "key": "261008161926.0.0", "started_at": f.at.Add(-50 * time.Minute), "at": f.at.Add(-10 * time.Minute), "outcome": "live"}}}
	var stored map[string]int
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/facts", release, 200, &stored)
	if stored["stored"] != 1 {
		t.Fatalf("stored: %v", stored)
	}
	// The same key corrects the earlier report instead of adding one.
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/facts", release, 200, &stored)
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/facts", map[string]any{"facts": []any{map[string]any{"kind": "merge_round", "key": "x", "at": f.at, "outcome": "model"}}}, 400, nil)
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
		readerRole, err := role("metrics_reader", "delivery.read", "nodes.read")
		if err != nil {
			return err
		}
		nodesRole, err := role("metrics_nodes", "nodes.read")
		if err != nil {
			return err
		}
		person := func(name string) (string, error) {
			var id string
			err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, f.person.TenantID, name).Scan(&id)
			return id, err
		}
		if reader, err = person("Project reader"); err != nil {
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
	path := "/api/projects/" + f.project + "/delivery/metrics"
	who := func(id string) tenant.Principal {
		return tenant.Principal{ID: id, TenantID: f.person.TenantID, Kind: tenant.Person}
	}
	var out DeliveryMetrics
	f.call(t, who(reader), "GET", path, nil, 200, &out)
	rel := metricByKey(t, out.Metrics, "release_queue_to_live")
	if rel.Latest == nil || rel.Latest.Value != 40 || rel.Windows[1].N != 1 {
		t.Fatalf("member reads the reported release: %+v", rel)
	}
	f.call(t, who(reader), "POST", "/api/projects/"+f.project+"/delivery/metrics/facts", release, 403, nil)
	f.call(t, who(nodesOnly), "GET", path, nil, 403, nil)
	f.call(t, who(outsider), "GET", path, nil, 404, nil)
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	// Row security keeps the facts themselves inside the project too.
	count := func(id string) int {
		var n int
		err := db.InTenant(tenant.WithPrincipal(t.Context(), who(id)), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_metric_marks)+(SELECT count(*) FROM delivery_metric_sources)`).Scan(&n)
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return n
	}
	if count(reader) != 2 || count(outsider) != 0 {
		t.Fatalf("row security: reader %d, other project %d", count(reader), count(outsider))
	}
}
