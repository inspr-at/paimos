// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/jackc/pgx/v5"
)

type learningProbeTx struct {
	pgx.Tx
	reads *int
	fail  bool
}

func (tx learningProbeTx) Begin(ctx context.Context) (pgx.Tx, error) {
	child, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return learningProbeTx{Tx: child, reads: tx.reads, fail: tx.fail}, nil
}
func (tx learningProbeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "WITH done AS") {
		*tx.reads++
	}
	if tx.fail && (strings.Contains(sql, "FROM outcome_events") || strings.Contains(sql, "FROM model_profiles\n WHERE family")) {
		// A real SQL error aborts the savepoint, proving transaction recovery.
		return tx.Tx.Query(ctx, "SELECT 1/0")
	}
	return tx.Tx.Query(ctx, sql, args...)
}
func TestPlanningLearningFailureDoesNotAbortWorkStart(t *testing.T) {
	w := planningSetup(t)
	n := placementNode(t, w, "HINTFAIL-1", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	reads := 0
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if err := CapturePlanningStart(t.Context(), learningProbeTx{Tx: tx, reads: &reads, fail: true}, n.ID, "session"); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(t.Context(), `SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, n.ID).Scan(&raw); err != nil {
			return err
		}
		var snap planningSnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return err
		}
		if snap.RateBasis.Basis != "default" || !strings.Contains(snap.RateBasis.BasisText, "history unavailable") || snap.Tokens == nil {
			t.Fatalf("failed hint baseline: %s", raw)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("learning read blocked work start: %v", err)
	}
}
func TestPlanningLearningReadOncePerPage(t *testing.T) {
	w := planningSetup(t)
	seedLearningTicket(t, w, 1, int64(3600000), false)
	n := placementNode(t, w, "ONCEREAD-1", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	page := listPage(t, w.admin, "/api/nodes?within="+w.root.ID+"&q="+n.Key)
	reads := 0
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := loadPlanning(t.Context(), learningProbeTx{Tx: tx, reads: &reads}, page.Items, assigneeSeen{harnessAll: true}, nil)
		return err
	})
	if err != nil || reads != 1 {
		t.Fatalf("page must share learning between figures and cost: reads=%d error=%v", reads, err)
	}
}
func TestPlanningLearningAnyRouteBasis(t *testing.T) {
	pl := &planner{calibrations: map[routeKey]calibration{}}
	for range 5 {
		pl.samples = append(pl.samples, calibrationSample{harness: "codex", model: "gpt-6-astra", tokensPerHour: 100})
	}
	basis := pl.calibration(nil).basisText
	if basis != "median of finished tickets on any route (n=5)" {
		t.Fatalf("empty route evidence: %q", basis)
	}
}
func TestPlanningLearningEpicExcludesDefaultChildren(t *testing.T) {
	w := planningSetup(t)
	for i := range 5 {
		seedLearningTicket(t, w, i+1, int64(3600000), false)
	}
	epic := w.node(t, "PARTIAL-1", "epic", w.root.ID, "open", nil)
	good := w.node(t, "PARTIAL-2", "ticket", epic.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "complexity": "L", "estimate_hours": 2})
	missing := w.node(t, "PARTIAL-3", "ticket", epic.ID, "open", map[string]any{"route_role": "build", "area": "frontend", "estimate_hours": 9})
	// Also a default-only ticket with a tiny size: it must still sort last.
	tiny := placementNode(t, w, "PARTIAL-4", map[string]any{"route_role": "build", "area": "frontend", "estimate_hours": 0.001})
	for _, sort := range []string{"tokens", "-tokens", "list_cost", "-list_cost"} {
		path := "/api/nodes?within=" + w.root.ID + "&state=open&sort=" + sort
		page := listPage(t, w.admin, path)
		for i, item := range page.Items {
			if item.ID == tiny.ID || item.ID == missing.ID {
				for _, later := range page.Items[i+1:] {
					if later.ID == good.ID || later.ID == epic.ID {
						t.Fatalf("hidden fallback sorts before known estimate (%s): %v", sort, page.Items)
					}
				}
			}
		}
		v := planningOf(t, w.admin, path)[epic.Key]
		raw, _ := json.Marshal(v.Children)
		var c map[string]int
		_ = json.Unmarshal(raw, &c)
		if c["estimated"] != 1 || c["total"] != 2 || c["uncalibrated"] != 1 || v.Tokens.Estimated == nil || *v.Tokens.Estimated != 4_000_000 || v.Cost.ListEstimated == nil || *v.Cost.ListEstimated != "10.800000" {
			t.Fatalf("partial epic (%s): %+v %s", sort, v, raw)
		}
	}
}

func TestPlanningLearningSnapshotSpeedReproducesFrozenTokens(t *testing.T) {
	w := planningSetup(t)
	for i := range 5 {
		seedLearningTicket(t, w, i+1, int64(4*3600000), false)
	}
	n := placementNode(t, w, "SPEEDBASIS-1", map[string]any{"route_role": "build-hard", "area": "backend", "complexity": "L", "estimate_hours": 3})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if err := CapturePlanningStart(t.Context(), tx, n.ID, "session"); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(t.Context(), `SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, n.ID).Scan(&raw); err != nil {
			return err
		}
		var frozen struct {
			Hours  float64 `json:"estimate_hours"`
			Tokens int64   `json:"estimated_tokens"`
			Rate   struct {
				Speed float64 `json:"speed"`
				Rate  float64 `json:"tokens_per_hour"`
			} `json:"rate_basis"`
		}
		if err := json.Unmarshal(raw, &frozen); err != nil {
			return err
		}
		if frozen.Rate.Speed != 2 || float64(frozen.Tokens) != frozen.Hours*frozen.Rate.Rate*frozen.Rate.Speed {
			t.Fatalf("missing/non-reproducible frozen speed: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Every added session is independently complete before changing exactly one
// exclusion dimension, so an incomplete run cannot explain the assertion.
func TestPlanningLearningExcludesMixedModelsPlacementsAndSource(t *testing.T) {
	for _, dimension := range []string{"model", "placement", "source"} {
		t.Run(dimension, func(t *testing.T) {
			w := planningSetup(t)
			first := seedLearningTicket(t, w, 1, int64(3600000), false)
			second := seedLearningTicket(t, w, 2, int64(3600000), false)
			foreign := w.node(t, "SOURCE-1", "project", "", "open", nil)
			w.session(t, first.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 4_000_000, 0, 0, "api", "")
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
				// Reuse the second ticket's distinct, fully measured run for the extra
				// first-ticket session; move its project to the same source as required.
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions extra SET run_id=other.run_id,
     model_profile_id=other.model_profile_id,created_at=original.created_at,
     heartbeat_at=original.heartbeat_at,stopped_at=original.stopped_at,work_placement=original.work_placement
     FROM harness_sessions original,harness_sessions other
     WHERE extra.ticket_node_id=$1 AND extra.run_id IS NULL AND original.ticket_node_id=$1 AND original.run_id IS NOT NULL AND other.ticket_node_id=$2`, first.ID, second.ID)
				if err != nil {
					return err
				}
				switch dimension {
				case "model":
					_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET model_profile_id=(SELECT id FROM model_profiles WHERE slug='codex-sol-xhigh'),model_raw='gpt-6-sol' WHERE ticket_node_id=$1 AND run_id=(SELECT run_id FROM harness_sessions WHERE ticket_node_id=$2)`, first.ID, second.ID)
				case "placement":
					_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET work_placement=jsonb_set(work_placement,'{bucket}','"normal"') WHERE ticket_node_id=$1 AND run_id=(SELECT run_id FROM harness_sessions WHERE ticket_node_id=$2)`, first.ID, second.ID)
				case "source":
					_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET project_id=$3 WHERE ticket_node_id=$1 AND run_id=(SELECT run_id FROM harness_sessions WHERE ticket_node_id=$2)`, first.ID, second.ID, foreign.ID)
				}
				if err != nil {
					return err
				}
				var complete int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions s JOIN agent_runs r ON r.id=s.run_id JOIN harness_session_usage u ON u.session_id=s.id WHERE s.ticket_node_id=$1 AND r.active_ms>0 AND r.status='completed' AND s.stopped_at IS NOT NULL AND NOT u.provisional AND u.input_tokens>0`, first.ID).Scan(&complete); err != nil {
					return err
				}
				if complete != 2 {
					t.Fatalf("fixture lacks two complete workers: %d", complete)
				}
				samples, err := usagedashboard.LoadLearningSamples(t.Context(), tx, []usagedashboard.LearningCell{{Family: "openai", Line: "astra", Effort: "xhigh", Kind: "backend", Bucket: "complex"}}, "", func(string) bool { return true })
				if err == nil && (len(samples) != 1 || samples[0].Hours != 1 || samples[0].Tokens != 4_000_000) {
					t.Fatalf("mixed %s history admitted: %+v", dimension, samples)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
