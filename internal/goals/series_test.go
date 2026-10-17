// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
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
