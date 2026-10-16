// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func detail(t *testing.T, r *httptest.ResponseRecorder) GoalView {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("detail %d %s", r.Code, r.Body)
	}
	var g GoalView
	if err := json.Unmarshal(r.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// Risk: hidden outcomes change the denominator, ETA, or project badges, while
// partial estimates incorrectly receive mean fill and queued hides a worker.
func TestGoalFilteredReadCoverageETAAndWorkerState(t *testing.T) {
	f := writerFixture(t)
	var leaf, agent string
	past := apiClock.Add(-PassedGrace - time.Second)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"state_set":"done"}' WHERE id=$1`, f.outcome); err != nil {
			return err
		}
		leaf = fixtureNode(t, tx, f.writer.TenantID, "work", "LEAF-1", f.ticket, map[string]any{"estimate_hours": 2})
		fixtureNode(t, tx, f.writer.TenantID, "work", "LEAF-2", f.ticket, nil)
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('eta_at',$2::text) WHERE id=$1`, f.keylessOutcome, past.Format(time.RFC3339)); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Worker') RETURNING id::text`, f.writer.TenantID).Scan(&agent)
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	path := "/api/goals/" + f.goal
	g := detail(t, goalRequest(t.Context(), m, f.reader, "GET", path, "", ""))
	if len(g.Items) != 2 || g.Counts.Outcomes != 2 || g.Progress == nil || g.Progress.Pct != 50 || g.Progress.Basis != "mixed" || g.Progress.ItemsPartiallyEstimated != 1 || g.Progress.ItemsWithoutEstimate != 1 {
		t.Fatalf("visible rollup %+v", g)
	}
	if g.ETA == nil || g.ETA.At == nil || !g.ETA.At.Equal(past) || g.Passed == nil || !*g.Passed || g.ETA.Partial {
		t.Fatalf("effective ETA %+v passed %v", g.ETA, g.Passed)
	}
	if len(g.Projects) != 1 || g.Projects[0].ID != f.project || strings.Contains(fmt.Sprint(g), "private-ETA") {
		t.Fatalf("hidden contribution leaked %+v", g)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$6,'working')`, f.writer.TenantID, f.project, agent, leaf, []byte("reference"), []byte("lease"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g = detail(t, goalRequest(t.Context(), m, f.reader, "GET", path, "", ""))
	for _, item := range g.Items {
		if item.ID == f.outcome && (item.State != Moving || len(item.SessionIDs) != 1) {
			t.Fatalf("worker lost to explicit state %+v", item)
		}
	}
	if g.Progress.Pct != 0 || g.States.Moving != 1 {
		t.Fatalf("worker rollup %+v", g.Progress)
	}
}

// Risk: an old response finishing last can overwrite a newer time-driven
// passed bit even though neither read caused an event.
func TestGoalReadGenerationOrdersReversedCompletion(t *testing.T) {
	f := writerFixture(t)
	at := apiClock.Add(-PassedGrace)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_build_object('eta_at',$3::text) WHERE tenant_id=$1 AND id=$2`, f.writer.TenantID, f.goal, at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	now := func() time.Time {
		if calls.Add(1) == 1 {
			return apiClock
		}
		return apiClock.Add(time.Second)
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool { return strings.Contains(sql, "nextval('goal_response_seq')") })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- goalRequest(ctx, &module{pool, now}, f.reader, "GET", "/api/goals/"+f.goal, "", "") }()
	barrier.Wait(t, ctx)
	newer := detail(t, goalRequest(ctx, &module{f.db.App, now}, f.reader, "GET", "/api/goals/"+f.goal, "", ""))
	barrier.Release()
	older := detail(t, dbtest.Await(t, ctx, done))
	if older.Passed == nil || *older.Passed || newer.Passed == nil || !*newer.Passed || older.ReadThrough != newer.ReadThrough || !older.Watermark.OlderThan(newer.Watermark) {
		t.Fatalf("reversed snapshots old=%+v new=%+v", older, newer)
	}
}

// Risk: the fixed threshold silently depends on reporting cadence, the job
// duplicates flips, or a later ETA sample fabricates history.
func TestGoalPassedRevalidationIsFixedAndIdempotent(t *testing.T) {
	f := writerFixture(t)
	at := apiClock.Add(-PassedGrace)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('eta_at',$3::text) WHERE tenant_id=$1 AND id=ANY($2::uuid[]);`, f.writer.TenantID, []string{f.goal, f.keylessOutcome}, at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	for _, now := range []time.Time{apiClock, apiClock.Add(time.Second), apiClock.Add(time.Second)} {
		if _, err := m.RevalidateTenant(t.Context(), f.writer.TenantID, "", now); err != nil {
			t.Fatal(err)
		}
	}
	var passed, etaEvents int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='goal.passed_changed'),count(*) FILTER(WHERE type='goal.eta_changed') FROM events WHERE tenant_id=$1`, f.writer.TenantID).Scan(&passed, &etaEvents); err != nil {
		t.Fatal(err)
	}
	if passed != 2 || etaEvents != 0 {
		t.Fatalf("passed flips %d ETA samples %d", passed, etaEvents)
	}
}

// Risk: a single large scope aborts the response or returns partial derived
// values as complete. A compact overlapping chain exceeds the real pair cap.
func TestGoalTooLargeDetailKeepsOnlyVisibleCounts(t *testing.T) {
	f := writerFixture(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		roots := []string{}
		parent := f.project
		for i := range 200 {
			parent = fixtureNode(t, tx, f.writer.TenantID, "work", fmt.Sprintf("BIG-%d", i+1), parent, nil)
			roots = append(roots, parent)
		}
		for i := range 260 {
			fixtureNode(t, tx, f.writer.TenantID, "work", fmt.Sprintf("BIG-%d", i+201), parent, nil)
		}
		for i := range 10 {
			o := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, fmt.Sprintf("BOUT-%d", i+1), f.secondGoal, map[string]any{"outcome_ref": fmt.Sprintf("part%d", i)})
			for _, root := range roots[i*20 : (i+1)*20] {
				if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.writer.TenantID, o, root); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := detail(t, apiRequest(t, f, http.MethodGet, "/api/goals/"+f.secondGoal, "", ""))
	if g.Expansion != "too_large" || g.Counts.Outcomes != 10 || g.Counts.Tickets != 200 || g.Progress != nil || g.ETA != nil || g.States != nil || len(g.Items) != 0 {
		t.Fatalf("oversize result %+v", g)
	}
	response := apiRequest(t, f, "GET", "/api/goals/"+f.secondGoal+"/series?range=24h", "", "")
	var series SeriesResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &series) != nil || series.Expansion != "too_large" || len(series.Points) > 0 {
		t.Fatalf("oversized series %d %s", response.Code, response.Body)
	}

}

// Risk: page/search/state filters alter totals, project filters omit workspace
// contributions, and history stops at a mutable goal/ref after a move.
func TestGoalListFiltersTotalsAndMoveHistory(t *testing.T) {
	f := writerFixture(t)
	body := `{"items":{"upsert":[{"ref":"workspace","keys":["TKT-1"],"state":"moving"}]}}`
	readWriteResult(t, apiRequest(t, f, "PATCH", "/api/goals/"+f.workspaceGoal, "workspace-up", body), 200)
	list := func(query string) GoalList {
		r := apiRequest(t, f, "GET", "/api/goals"+query, "", "")
		if r.Code != 200 {
			t.Fatalf("list %d %s", r.Code, r.Body)
		}
		var v GoalList
		json.Unmarshal(r.Body.Bytes(), &v)
		return v
	}
	first := list("?project=" + f.project + "&sort=name&limit=1")
	if first.NextCursor == "" {
		t.Fatal("missing cursor")
	}
	second := list("?project=" + f.project + "&sort=name&limit=1&cursor=" + first.NextCursor)
	if !reflect.DeepEqual(first.Totals, second.Totals) || !reflect.DeepEqual(first.QuietProjects, second.QuietProjects) {
		t.Fatal("page changed global totals")
	}
	queued := list("?project=" + f.project + "&state=queued")
	if queued.Totals != first.Totals {
		t.Fatal("state filter changed totals")
	}
	visibleWorkspace := false
	for _, g := range list("?project=" + f.project).Items {
		visibleWorkspace = visibleWorkspace || g.ID == f.workspaceGoal
	}
	if !visibleWorkspace {
		t.Fatal("project filter lost contributing workspace goal")
	}
	for _, sortBy := range []string{"eta", "name", "progress", "project", "moving", "queued", "done"} {
		list("?sort=" + sortBy)
	}
	if apiRequest(t, f, "GET", "/api/goals?sort=unknown", "", "").Code != 400 {
		t.Fatal("unknown sort accepted")
	}
	move := `{"items":{"move":[{"outcome_id":"` + f.outcome + `","to_goal":"` + f.secondGoal + `"}]}}`
	readWriteResult(t, apiRequest(t, f, "PATCH", "/api/goals/"+f.goal, "history-move", move), 200)
	r := apiRequest(t, f, "GET", "/api/goals/"+f.secondGoal+"/history?outcome="+f.outcome, "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "goal.items_changed") || !strings.Contains(r.Body.String(), f.goal) || !strings.Contains(r.Body.String(), f.secondGoal) {
		t.Fatalf("move history %d %s", r.Code, r.Body)
	}
}

// Risk: an old paused session leaves an outcome Paused after a newer failed
// worker has stopped. The latest stop, not any historical pause, decides it.
func TestGoalPauseUsesLatestStoppedSession(t *testing.T) {
	f := writerFixture(t)
	var agent, latest string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Pause worker') RETURNING id::text`, f.writer.TenantID).Scan(&agent); err != nil {
			return err
		}
		for i, reason := range []string{"paused", "process_failed"} {
			var id string
			var pause any
			if reason == "paused" {
				pause = `{"state":"paused"}`
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,stopped_at,stop_reason,pause_record) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$6,$7,$8,$9::jsonb) RETURNING id::text`, f.writer.TenantID, f.project, agent, f.ticket, []byte(fmt.Sprintf("pause-ref-%d", i)), []byte(fmt.Sprintf("pause-lease-%d", i)), apiClock.Add(time.Duration(i-2)*time.Hour), reason, pause).Scan(&id); err != nil {
				return err
			}
			if i == 1 {
				latest = id
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	readState := func() State {
		g := detail(t, goalRequest(t.Context(), m, f.reader, "GET", "/api/goals/"+f.goal, "", ""))
		for _, item := range g.Items {
			if item.ID == f.outcome {
				return item.State
			}
		}
		t.Fatal("missing visible outcome")
		return ""
	}
	if state := readState(); state != Queued {
		t.Fatalf("old pause won: %s", state)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=$2 WHERE id=$1`, latest, apiClock.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if state := readState(); state != Paused {
		t.Fatalf("latest pause lost: %s", state)
	}
}
