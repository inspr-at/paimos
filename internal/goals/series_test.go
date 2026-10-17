// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sourceJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Risk: membership is sampled from the current parent, or a 201-hour planner
// span expands into more than 200 points. Both boundaries use an injected clock.
func TestGoalSeriesPlannerAndMoveTimeline(t *testing.T) {
	now := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	for span, count := range map[string]int{"24h": 24, "7d": 28, "30d": 30} {
		points, _, err := planSeries(now, span)
		if err != nil || len(points) != count {
			t.Fatalf("planner %s %d %v", span, len(points), err)
		}
	}
	points, width := boundedSeriesBuckets(now, 201*time.Hour, time.Hour)
	if len(points) > 200 || width != 6*time.Hour {
		t.Fatalf("unbounded planner %d %v", len(points), width)
	}
	outcome := storedNode{ID: "outcome", Parent: "source", Fields: map[string]any{"outcome_ref": "one", "state_set": "done"}, Targets: []string{}}
	h := reconstruction{goal: "source", created: now.Add(-24 * time.Hour), metadata: map[string]historicalNode{}, kinds: map[string]bool{}, events: []sourceEvent{{ID: 1, At: now.Add(-3 * time.Hour), Node: outcome.ID, Family: "outcome", After: sourceJSON(t, outcome)}}}
	outcome.Parent = "destination"
	h.events = append(h.events, sourceEvent{ID: 2, At: now.Add(-time.Hour), Node: outcome.ID, Family: "outcome", After: sourceJSON(t, outcome)})
	source, _, err := h.project(context.Background(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	h.goal = "destination"
	destination, _, err := h.project(context.Background(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	// The 17:00 bucket's as-of is 18:00 (before the move); 18:00's is
	// 19:00 (the move instant, which belongs to the destination).
	for _, check := range []struct {
		points []SeriesPoint
		asOf   time.Time
		want   int
	}{{source, now.Add(-2 * time.Hour), 100}, {source, now.Add(-time.Hour), 0}, {destination, now.Add(-2 * time.Hour), 0}, {destination, now.Add(-time.Hour), 100}} {
		found := false
		for _, point := range check.points {
			if point.T.Add(time.Hour).Equal(check.asOf) {
				found = true
				if point.Pct != check.want {
					t.Fatalf("asof %v pct=%d want=%d", check.asOf, point.Pct, check.want)
				}
			}
		}
		if !found {
			t.Fatal("missing move boundary bucket")
		}
	}
}

// Risk: hidden completion changes historical progress or stall.since; a live
// row is used instead of recorded facts; the four-hour comparison is exclusive.
func TestGoalSeriesVisibilityAndFourHourStall(t *testing.T) {
	f := writerFixture(t)
	config := f.db.App.Config()
	config.ConnConfig.Tracer = goalQueryDiagnostics{t}
	readPool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readPool.Close)
	now := apiClock
	start := now.Add(-12 * time.Hour)
	change := now.Add(-4 * time.Hour)
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET created_at=$2 WHERE id=$1`, f.secondGoal, now.Add(-24*time.Hour)); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"estimate_hours":2}',state='done' WHERE id=$1`, f.ticket); err != nil {
			return err
		}
		subjects := []struct {
			ref, key, target, state string
			at                      time.Time
		}{{"visible", "SOUT-1", f.ticket, "", start}, {"moving", "SOUT-2", "", "moving", start}, {"hidden", "SOUT-3", f.hiddenTicket, "done", now.Add(-time.Hour)}}
		for _, s := range subjects {
			fields := map[string]any{"outcome_ref": s.ref}
			if s.state != "" {
				fields["state_set"] = s.state
			}
			id := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, s.key, f.secondGoal, fields)
			if s.target != "" {
				if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.writer.TenantID, id, s.target); err != nil {
					return err
				}
			}
			n, err := resolveNode(t.Context(), tx, id, false)
			if err != nil {
				return err
			}
			n.Targets = []string{}
			if s.target != "" {
				n.Targets = append(n.Targets, s.target)
			}
			if _, err := events.Append(t.Context(), tx, f.writer, events.Change{NodeID: &id, Type: "goal.items_changed", After: n, At: &s.at}); err != nil {
				return err
			}
		}
		initial := map[string]any{"id": f.ticket, "key": "TKT-1", "parent_id": f.project, "fields": map[string]any{"estimate_hours": 2}, "state": "open", "deleted_at": nil}
		if _, err := events.Append(t.Context(), tx, f.writer, events.Change{NodeID: &f.ticket, Type: "node.created", After: initial, At: &start}); err != nil {
			return err
		}
		after := map[string]any{"id": f.ticket, "state": "done"}
		_, err := events.Append(t.Context(), tx, f.writer, events.Change{NodeID: &f.ticket, Type: "node.updated", Before: map[string]any{"id": f.ticket, "state": "open"}, After: after, At: &change})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(at time.Time) GoalView {
		return detail(t, goalRequest(t.Context(), &module{readPool, func() time.Time { return at }}, f.reader, "GET", "/api/goals/"+f.secondGoal, "", ""))
	}
	before := get(now.Add(-time.Second))
	atBoundary := get(now)
	if before.Stall == nil || before.Stall.Stalled || atBoundary.Stall == nil || !atBoundary.Stall.Stalled || !atBoundary.Stall.Since.Equal(change) {
		t.Fatalf("stall boundary before=%+v after=%+v", before.Stall, atBoundary.Stall)
	}
	list := listResult(t, f, &module{readPool, func() time.Time { return now }}, "?project="+f.project)
	found := false
	for _, g := range list.Items {
		if g.ID == f.secondGoal {
			found = true
			if g.Progress == nil || g.Progress.Pct != 50 || !reflect.DeepEqual(g.Stall, atBoundary.Stall) {
				t.Fatalf("page history differs from detail: %+v", g)
			}
		}
	}
	if !found {
		t.Fatal("list omitted the goal with recorded history")
	}
	for span, count := range map[string]int{"24h": 24, "7d": 28, "30d": 30} {
		r := goalRequest(t.Context(), &module{readPool, func() time.Time { return now }}, f.reader, "GET", "/api/goals/"+f.secondGoal+"/series?range="+span, "", "")
		if r.Code != 200 {
			t.Fatalf("series %d %s", r.Code, r.Body)
		}
		var s SeriesResult
		if err := json.Unmarshal(r.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if len(s.Points) != count || s.Points[len(s.Points)-1].Pct != 50 {
			t.Fatalf("filtered series %s %+v", span, s)
		}
		changed := false
		for i, point := range s.Points {
			if i > 0 && point.Delta != point.Pct-s.Points[i-1].Pct {
				t.Fatalf("incorrect bucket delta %+v", point)
			}
			if !reflect.DeepEqual(point.Keys, point.ChangedKeys) || slices.Contains(point.ChangedKeys, "SOUT-3") || slices.Contains(point.ChangedKeys, "TKT-2") {
				t.Fatalf("hidden keys or mismatched compatibility alias %+v", point)
			}
			changed = changed || slices.Contains(point.ChangedKeys, "TKT-1") && slices.Contains(point.ChangedKeys, "SOUT-1")
		}
		if !changed {
			t.Fatalf("missing visible ticket and outcome transitions: %+v", s.Points)
		}
	}
	// Mutate the live row without an event: historical reconstruction must
	// retain the recorded completion, proving it is not a live-row sample.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='open' WHERE tenant_id=$1 AND id=$2`, f.writer.TenantID, f.ticket); err != nil {
		t.Fatal(err)
	}
	r := goalRequest(t.Context(), &module{readPool, func() time.Time { return now }}, f.reader, "GET", "/api/goals/"+f.secondGoal+"/series?range=24h", "", "")
	var s SeriesResult
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &s) != nil || len(s.Points) != 24 || s.Points[23].Pct != 50 {
		t.Fatalf("sampled live state %d %s", r.Code, r.Body)
	}
	for _, query := range []string{"?range=201h", "?range=24h&range=7d"} {
		if rec := apiRequest(t, f, "GET", "/api/goals/"+f.secondGoal+"/series"+query, "", ""); rec.Code != 400 {
			t.Fatalf("unbounded range %s %d", query, rec.Code)
		}
	}
}

// Risk: a queued-to-moving change disappears because progress remains zero,
// or the first bucket's delta loses the preceding bucket's value.
func TestGoalSeriesChangedKeysWithoutPercentageChange(t *testing.T) {
	now := apiClock.Add(30 * time.Minute)
	starts, _, _ := planSeries(now, "24h")
	outcome := storedNode{ID: "outcome", Key: "OUT-1", Parent: "goal", Fields: map[string]any{"state_set": "done"}}
	h := reconstruction{goal: "goal", created: starts[0].Add(-time.Hour), metadata: map[string]historicalNode{}, events: []sourceEvent{
		{ID: 1, At: starts[0], Node: outcome.ID, Family: "outcome", After: sourceJSON(t, outcome)},
	}}
	outcome.Fields["state_set"] = "queued"
	h.events = append(h.events, sourceEvent{ID: 2, At: starts[0].Add(time.Minute), Node: outcome.ID, Family: "outcome", After: sourceJSON(t, outcome)})
	outcome.Fields["state_set"] = "moving"
	h.events = append(h.events, sourceEvent{ID: 3, At: starts[1].Add(time.Minute), Node: outcome.ID, Family: "outcome", After: sourceJSON(t, outcome)})
	points, since, err := h.project(t.Context(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if points[0].Delta != -100 || points[1].Delta != 0 || !reflect.DeepEqual(points[1].ChangedKeys, []string{"OUT-1"}) || !since.Equal(h.events[1].At) {
		t.Fatalf("state-only transition or prefix delta lost: points=%+v since=%v", points[:2], since)
	}
}

// Keep SQL text (never arguments/credentials) on a failed regression so a
// bounded request failure identifies the expensive visibility projection.
type goalQueryDiagnostics struct{ t *testing.T }
type goalQueryDiagnosticKey struct{}
type goalQueryDiagnosticStart struct {
	at  time.Time
	sql string
}

func (q goalQueryDiagnostics) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, goalQueryDiagnosticKey{}, goalQueryDiagnosticStart{time.Now(), data.SQL})
}
func (q goalQueryDiagnostics) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(goalQueryDiagnosticKey{}).(goalQueryDiagnosticStart)
	if !ok {
		return
	}
	elapsed := time.Since(start.at)
	if elapsed > 200*time.Millisecond || data.Err != nil {
		sql := start.sql
		if len(sql) > 1800 {
			sql = sql[:1800]
		}
		q.t.Logf("goal query %s err=%v SQL=%s", elapsed, data.Err, sql)
	}
}

func seedRecordedGoal(t *testing.T, f storageFixture, start time.Time) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET created_at=$2 WHERE id=$1`, f.secondGoal, start.Add(-time.Hour)); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"estimate_hours":2}',state='open' WHERE id=$1`, f.ticket); err != nil {
			return err
		}
		served := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "RECOUT-1", f.secondGoal, map[string]any{"outcome_ref": "served"})
		moving := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "RECOUT-2", f.secondGoal, map[string]any{"outcome_ref": "moving", "state_set": "moving"})
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.writer.TenantID, served, f.ticket); err != nil {
			return err
		}
		pending := []events.Change{}
		for _, id := range []string{served, moving} {
			n, err := resolveNode(t.Context(), tx, id, false)
			if err != nil {
				return err
			}
			if id == served {
				n.Targets = []string{f.ticket}
			}
			pending = append(pending, events.Change{NodeID: &n.ID, Type: "goal.items_changed", After: n, At: &start})
		}
		pending = append(pending, events.Change{NodeID: &f.ticket, Type: "node.created", At: &start, After: map[string]any{"id": f.ticket, "key": "TKT-1", "parent_id": f.project, "state": "open", "fields": map[string]any{"estimate_hours": 2}, "deleted_at": nil}})
		for _, change := range pending {
			if _, err := events.Append(t.Context(), tx, f.writer, change); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Explain the real statements before executing them, never a copied query.
// Deterministic row visits reject an unrelated-history scan on fast machines.
type goalHistoryPlanTx struct {
	pgx.Tx
	t      *testing.T
	plans  int
	visits float64
}

func relationPlanVisits(v any, relation string) float64 {
	var total float64
	switch n := v.(type) {
	case []any:
		for _, child := range n {
			total += relationPlanVisits(child, relation)
		}
	case map[string]any:
		if n["Relation Name"] == relation {
			rows, _ := n["Actual Rows"].(float64)
			removed, _ := n["Rows Removed by Filter"].(float64)
			loops, _ := n["Actual Loops"].(float64)
			total += (rows + removed) * loops
		}
		total += relationPlanVisits(n["Plan"], relation) + relationPlanVisits(n["Plans"], relation)
	}
	return total
}

func (tx *goalHistoryPlanTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "events") || strings.Contains(sql, "aeon_goal_history_children") {
		var raw []byte
		if err := tx.Tx.QueryRow(ctx, "EXPLAIN (ANALYZE,FORMAT JSON,TIMING OFF) "+sql, args...).Scan(&raw); err != nil {
			return nil, err
		}
		var plan any
		if err := json.Unmarshal(raw, &plan); err != nil {
			return nil, err
		}
		visits := relationPlanVisits(plan, "events")
		tx.visits += visits
		tx.plans++
		tx.t.Logf("history statement %d event visits=%.0f", tx.plans, visits)
		if visits > 100 {
			tx.t.Errorf("history statement visits=%.0f; want at most 100 candidate event visits, excluding 6000 unrelated records", visits)
		}
	}
	return tx.Tx.Query(ctx, sql, args...)
}

// Risk: detail/series/list read all old node and harness events even when the
// candidate goal has only three records. Preserve an older-than-range baseline
// while proving unrelated history does not grow any real statement's scans.
func TestGoalHistoryIgnoresUnrelatedEvents(t *testing.T) {
	f := writerFixture(t)
	seedRecordedGoal(t, f, apiClock.Add(-96*time.Hour))
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after,at)
 SELECT $1,$2,CASE WHEN g%3=0 THEN $3::uuid ELSE NULL END,
 CASE g%3 WHEN 0 THEN 'node.created' WHEN 1 THEN 'harness.heartbeat' ELSE 'node.bulk_changed' END,
 CASE g%3 WHEN 0 THEN jsonb_build_object('id',$3::text,'parent_id',$4::text,'state','done','fields','{}'::jsonb)
 WHEN 1 THEN jsonb_build_object('id',gen_random_uuid(),'ticket_node_id',$3::text,'phase','working','role','worker')
 ELSE jsonb_build_object('items',jsonb_build_array(jsonb_build_object('id',$3::text,'parent_id',$4::text,'state','done','fields','{}'::jsonb))) END,
 CASE WHEN g<=5000 THEN $5::timestamptz-interval '180 days' ELSE $5::timestamptz-interval '1 hour' END
 FROM generate_series(1,6000) g`, f.writer.TenantID, f.writer.ID, f.hiddenTicket, f.otherProject, apiClock)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `ANALYZE events; ANALYZE nodes`); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), f.writer)
	err = db.InTenant(ctx, f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&count); err != nil {
			return err
		}
		if count < 6003 {
			return fmt.Errorf("unrelated history fixture missing: %d", count)
		}
		// Explain the installed reference helper's query too. Otherwise an
		// opaque function scan could hide unrelated work from the counters.
		var definition string
		if err := tx.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid='aeon_goal_history_refs(uuid,text,timestamptz,timestamptz)'::regprocedure`).Scan(&definition); err != nil {
			return err
		}
		_, referenceSQL, ok := strings.Cut(definition, "SELECT r.event_id")
		if !ok {
			return fmt.Errorf("installed reference query missing")
		}
		referenceSQL, _, ok = strings.Cut(referenceSQL, ") e;")
		if !ok {
			return fmt.Errorf("installed reference query boundary missing")
		}
		referenceSQL = "SELECT r.event_id" + strings.NewReplacer("p_node", "$1::uuid", "p_type", "$2::text", "p_from", "$3::timestamptz", "p_until", "$4::timestamptz").Replace(referenceSQL)
		var raw []byte
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE,FORMAT JSON,TIMING OFF) "+referenceSQL, f.ticket, "node.%", apiClock.Add(-120*time.Hour), apiClock).Scan(&raw); err != nil {
			return err
		}
		var referencePlan any
		if err := json.Unmarshal(raw, &referencePlan); err != nil {
			return err
		}
		refVisits, eventVisits := relationPlanVisits(referencePlan, "goal_event_refs"), relationPlanVisits(referencePlan, "events")
		t.Logf("installed reference helper visits: references=%.0f events=%.0f", refVisits, eventVisits)
		if refVisits <= 0 || refVisits > 10 || eventVisits <= 0 || eventVisits > 10 {
			t.Errorf("reference helper scans unrelated history: references=%.0f events=%.0f", refVisits, eventVisits)
		}
		traced := &goalHistoryPlanTx{Tx: tx, t: t}
		h, err := loadReconstruction(ctx, traced, f.secondGoal, apiClock)
		if err != nil {
			return err
		}
		points, since, err := h.project(ctx, apiClock, "24h")
		if err != nil {
			return err
		}
		if len(h.events) != 3 || len(points) != 24 || points[23].Pct != 0 || !since.Equal(apiClock.Add(-97*time.Hour)) {
			t.Fatalf("recorded prefix lost: events=%d points=%d since=%v", len(h.events), len(points), since)
		}
		page, err := loadListPage(ctx, traced, f.writer, []string{f.secondGoal}, apiClock, Watermark{})
		if err != nil {
			return err
		}
		if len(page) != 1 || page[0].Expansion != "full" || page[0].Stall == nil || !page[0].Stall.Since.Equal(since) {
			t.Fatalf("page prefix differs from series: %+v", page)
		}
		if traced.plans < 5 || traced.visits <= 0 {
			t.Fatalf("missing positive history plan evidence: plans=%d visits=%.0f", traced.plans, traced.visits)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Risk: batch snapshots, their undo, and wrapped project moves vanish from the
// series and falsely mark a moving goal stalled. Only recorded snapshots drive
// the three transitions; the reader is subject to the real event/node RLS.
func TestGoalSeriesBulkUndoAndProjectSnapshots(t *testing.T) {
	t.Run("atomic batch preserves unchanged percentage", func(t *testing.T) {
		start, batchAt := apiClock.Add(-12*time.Hour), apiClock.Add(-time.Hour)
		h := reconstruction{goal: "goal", created: start, metadata: map[string]historicalNode{
			"a": {ID: "a", Key: "TKT-A", Work: true}, "b": {ID: "b", Key: "TKT-B", Work: true},
		}}
		for i, id := range []string{"a", "b"} {
			o := storedNode{ID: "outcome-" + id, Key: "OUT-" + id, Parent: "goal", Fields: map[string]any{}, Targets: []string{id}}
			h.events = append(h.events, sourceEvent{ID: int64(i + 1), At: start, Node: o.ID, Family: "outcome", After: sourceJSON(t, o)})
		}
		done, open := "done", "open"
		for i, id := range []string{"a", "b"} {
			bucket := &done
			if id == "b" {
				bucket = &open
			}
			h.events = append(h.events, sourceEvent{ID: int64(i + 3), At: start, Node: id, Family: "node", After: sourceJSON(t, map[string]any{"state": *bucket}), Bucket: bucket})
		}
		h.events = append(h.events,
			sourceEvent{ID: 5, At: batchAt, Node: "a", Family: "node", After: sourceJSON(t, map[string]any{"state": open}), Bucket: &open},
			sourceEvent{ID: 5, At: batchAt, Node: "b", Family: "node", After: sourceJSON(t, map[string]any{"state": done}), Bucket: &done})
		points, since, err := h.project(t.Context(), apiClock, "24h")
		if err != nil || !since.Equal(start) || len(points) != 24 || points[23].Pct != 50 {
			t.Fatalf("atomic batch invented percentage change: since=%v points=%v err=%v", since, points, err)
		}
	})
	f := writerFixture(t)
	seedRecordedGoal(t, f, apiClock.Add(-12*time.Hour))
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE id=$1`, f.ticket); err != nil {
			return err
		}
		open := map[string]any{"id": f.ticket, "key": "TKT-1", "parent_id": f.project, "fields": map[string]any{"estimate_hours": 2}, "state": "open"}
		done := map[string]any{"id": f.ticket, "key": "TKT-1", "parent_id": f.project, "fields": map[string]any{"estimate_hours": 2}, "state": "done"}
		batch := func(n any) any { return map[string]any{"items": []any{n}} }
		closedAt, undoAt, movedAt := apiClock.Add(-3*time.Hour), apiClock.Add(-2*time.Hour), apiClock.Add(-time.Hour)
		closed, err := events.Append(t.Context(), tx, f.reader, events.Change{Type: "node.bulk_changed", Before: batch(open), After: batch(done), At: &closedAt})
		if err != nil {
			return err
		}
		if _, err := events.Append(t.Context(), tx, f.reader, events.Change{Type: "node.bulk_changed", Before: batch(done), After: batch(open), UndoOf: &closed.ID, At: &undoAt}); err != nil {
			return err
		}
		_, err = events.Append(t.Context(), tx, f.reader, events.Change{NodeID: &f.ticket, Type: "node.project_moved", Before: map[string]any{"node": open}, After: map[string]any{"node": done}, At: &movedAt})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	rec := goalRequest(t.Context(), m, f.reader, "GET", "/api/goals/"+f.secondGoal+"/series?range=24h", "", "")
	var series SeriesResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &series) != nil || len(series.Points) != 24 {
		t.Fatalf("series failed: %d %s", rec.Code, rec.Body)
	}
	for _, expected := range []struct{ hours, pct, delta int }{{3, 50, 50}, {2, 0, -50}, {1, 50, 50}} {
		asOf := apiClock.Add(-time.Duration(expected.hours) * time.Hour)
		found := false
		for _, point := range series.Points {
			if point.T.Add(time.Hour).Equal(asOf) {
				found = true
				if point.Pct != expected.pct || point.Delta != expected.delta || !slices.Contains(point.ChangedKeys, "TKT-1") || !slices.Contains(point.ChangedKeys, "RECOUT-1") {
					t.Errorf("missing normalized snapshot at %v: %+v", asOf, point)
				}
			}
		}
		if !found {
			t.Errorf("missing bucket at %v", asOf)
		}
	}
	check := func(g GoalView) {
		if g.Progress == nil || g.Progress.Pct != 50 || g.Stall == nil || g.Stall.Stalled || !g.Stall.Since.Equal(apiClock.Add(-time.Hour)) {
			t.Errorf("progress/stall lost batch or project move: %+v", g)
		}
	}
	check(detail(t, goalRequest(t.Context(), m, f.reader, "GET", "/api/goals/"+f.secondGoal, "", "")))
	page := listResult(t, f, m, "?project="+f.project)
	found := false
	for _, g := range page.Items {
		if g.ID == f.secondGoal {
			found = true
			check(g)
		}
	}
	if !found {
		t.Fatal("list omitted recorded goal")
	}
}

type goalHistoryCancelTx struct {
	pgx.Tx
	injected *bool
}

func (tx *goalHistoryCancelTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	return &goalHistoryCancelTx{nested, tx.injected}, err
}
func (tx *goalHistoryCancelTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "subjects AS MATERIALIZED") {
		*tx.injected = true
		return nil, &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	}
	return tx.Tx.Query(ctx, sql, args...)
}

// Risk: the explicit PostgreSQL statement budget becomes an internal error
// instead of the documented too_large expansion, losing a readable goal.
func TestGoalHistoryCancellationReturnsTooLarge(t *testing.T) {
	f := writerFixture(t)
	seedRecordedGoal(t, f, apiClock.Add(-12*time.Hour))
	ctx := tenant.WithPrincipal(t.Context(), f.reader)
	err := db.InTenant(ctx, f.db.App, f.reader.TenantID, func(tx pgx.Tx) error {
		n, err := resolveNode(ctx, tx, f.secondGoal, false)
		if err != nil {
			return err
		}
		injected := false
		g, err := readGoal(ctx, &goalHistoryCancelTx{tx, &injected}, f.reader, n, apiClock, Watermark{ReadThrough: 7, Generation: 9})
		if err != nil {
			return err
		}
		if !injected || g.ID != n.ID || g.Expansion != "too_large" || g.Counts.Outcomes != 2 || g.Progress != nil || g.Stall != nil || g.ReadThrough != 7 || g.Generation != 9 {
			t.Fatalf("cancellation discarded goal or reported partial success: injected=%t goal=%+v", injected, g)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
