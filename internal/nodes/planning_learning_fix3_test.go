// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
 "fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPlanningDegradedHistoryRetainsLegacyCalibration(t *testing.T) {
	for _, issue := range []string{"history truncated", "history unavailable"} {
		for _, routed := range []bool{false, true} {
			for _, count := range []int{0, 4, 5} {
				t.Run(fmtDegradedCase(issue, routed, count), func(t *testing.T) {
					pl := &planner{learningIssue: issue, calibrations: map[routeKey]calibration{}}
					price := 7.0
					for range count {
						pl.samples = append(pl.samples, calibrationSample{harness: "codex", model: "gpt-6-astra", effort: "xhigh", tokensPerHour: 2_000_000, listPerHour: &price})
					}
					var route *planRoute
					if routed {
						route = &planRoute{key: routeKey{harness: "codex", model: "gpt-6-astra", effort: "xhigh"}}
					}
					got := pl.calibration(route)
					if count >= calibrationMinimum {
						if got.basis != "median" || got.level != "route" || got.tickets != count || got.tokensPerHour != 2_000_000 || got.listPerHour == nil || *got.listPerHour != price {
							t.Fatalf("degraded history discarded bounded legacy evidence: %+v", got)
						}
					} else if got.basis != "default" || got.level != "default" || got.tickets != count || got.tokensPerHour != defaultTokensPerHour {
						t.Fatalf("degraded fallback lost available sample count: %+v", got)
					}
					if got.speed != 1 || !strings.Contains(got.basisText, issue) || (routed && !strings.Contains(got.basisText, "on route codex gpt-6-astra xhigh") && count >= 5) || (!routed && !strings.Contains(got.basisText, "on any route") && count >= 5) {
						t.Fatalf("degraded basis must name independent evidence and issue: %+v", got)
					}
				})
			}
		}
	}
}

func fmtDegradedCase(issue string, routed bool, count int) string {
return fmt.Sprintf("%s/route=%t/samples=%d", issue, routed, count)
}

func TestPlanningLearningFailureLogsSafeContextOnce(t *testing.T) {
	w := planningSetup(t)
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	reads := 0
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		pl := &planner{routes: map[string]*planRoute{"target": {view: &planningRoute{Harness: "codex"}}}, calibrations: map[routeKey]calibration{}}
		if err := pl.loadLearning(t.Context(), learningProbeTx{Tx: tx, reads: &reads, fail: true}, func(string) bool { return true }, w.root.ID); err != nil {
			return err
		}
		pl.calibration(nil)
		pl.calibration(nil)
		return nil
	})
	if err != nil { t.Fatal(err) }
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 1 { t.Fatalf("expected one safe diagnostic: %q", log.String()) }
	var entry map[string]any
	if json.Unmarshal([]byte(lines[0]), &entry) != nil || entry["msg"] != "planning learning history unavailable" || entry["project_id"] != w.root.ID || entry["sqlstate"] != "22012" || entry["target_cells"] != float64(1) {
		t.Fatalf("missing safe planning context: %q", log.String())
	}
	if strings.Contains(log.String(), "SELECT") || strings.Contains(log.String(), "division by zero") { t.Fatal("raw SQL or error text leaked into diagnostic") }
}

type planningReadTracer struct {
	profiles, candidates, aggregates atomic.Int32
}
func (tr *planningReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM model_profiles\n WHERE family") { tr.profiles.Add(1) }
	if strings.Contains(data.SQL, "FROM outcome_events o JOIN nodes") && !strings.Contains(data.SQL, "WITH done AS") { tr.candidates.Add(1) }
	if strings.Contains(data.SQL, "WITH done AS") { tr.aggregates.Add(1) }
	return ctx
}
func (*planningReadTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func planningTracedModule(t *testing.T) (*Module, *planningReadTracer) {
	t.Helper()
	config := appPool.Config()
	trace := &planningReadTracer{}
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil { t.Fatal(err) }
	t.Cleanup(pool.Close)
	return New(pool, nil).(*Module), trace
}

func TestPlanningSortedListSharesLearningRead(t *testing.T) {
	w := planningSetup(t)
	for i := range 5 { seedLearningTicket(t, w, i+1, int64(3600000), false) }
	n := placementNode(t, w, "SORTREAD-1", map[string]any{"route_role": "build-hard", "area": "backend", "complexity": "L", "estimate_hours": 2})
	mod, trace := planningTracedModule(t)
	status, raw := callAs(t, mod, &w.admin, http.MethodGet, "/api/nodes?within="+w.root.ID+"&state=open&kind=ticket&sort=-tokens", "")
	page := decode[nodePage](t, status, raw, http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].ID != n.ID || page.Items[0].Planning.Tokens.Calibration.Level != "cell" || trace.profiles.Load() != 1 || trace.candidates.Load() != 1 || trace.aggregates.Load() != 1 {
		t.Fatalf("sorted list did not share its planner: profiles=%d candidates=%d aggregates=%d response=%s", trace.profiles.Load(), trace.candidates.Load(), trace.aggregates.Load(), raw)
	}
}

func TestPlanningTruncatedHistoryListFixture(t *testing.T) {
	w := planningSetup(t)
	// Independent legacy history is measured and bounded, even when the model
	// registry makes the new learning query hit its real 4096-profile cap.
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("LEGACY-%d", i+1), "ticket", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 4_000_000, 0, 0, "api", "")
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at='2026-09-01T12:00:00Z',heartbeat_at='2026-09-01T13:00:00Z',stopped_at='2026-09-01T13:00:00Z' WHERE ticket_node_id=$1`, n.ID)
			return err
		}); err != nil { t.Fatal(err) }
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
 SELECT $1,'history-cap-'||i,'1','codex','openai','gpt-6-astra','xhigh','standard' FROM generate_series(1,4097) i`, w.admin.TenantID)
		return err
	}); err != nil { t.Fatal(err) }
	target := placementNode(t, w, "TRUNCATED-1", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	mod, trace := planningTracedModule(t)
	status, raw := callAs(t, mod, &w.admin, http.MethodGet, "/api/nodes?within="+w.root.ID+"&state=open&kind=ticket&sort=-tokens", "")
	page := decode[nodePage](t, status, raw, http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].ID != target.ID { t.Fatalf("truncated list: %s", raw) }
	p := page.Items[0].Planning
	if p == nil || p.Tokens.Calibration.Basis != "median" || p.Tokens.Calibration.Level != "route" || p.Tokens.Calibration.Tickets != 5 || p.Tokens.Estimated == nil || *p.Tokens.Estimated != 8_000_000 || !strings.Contains(p.Tokens.Calibration.BasisText, "history truncated") || p.Cost == nil || p.Cost.ListEstimated == nil || *p.Cost.ListEstimated != "80.000000" {
		t.Fatalf("real cap discarded independent route estimates: %s", raw)
	}
	if trace.profiles.Load() != 1 || trace.candidates.Load() != 0 || trace.aggregates.Load() != 0 { t.Fatalf("cap not bounded before expensive work: %+v", trace) }
	if *capturePlanningListFixture { t.Logf("TRUNCATED_PLANNING_LIST_FIXTURE=%s", base64.StdEncoding.EncodeToString(raw)); return }
	want, err := os.ReadFile("../../web/tests/fixtures/planning-truncated-list.json")
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(planningFixtureProjection(t, raw), planningFixtureProjection(t, want)) { t.Fatalf("truncated API response differs from shared UI fixture: %s", raw) }
}

