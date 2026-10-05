// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Capture the executed count query rather than duplicating its implementation.
type recoveryQueryRecorder struct {
	pgx.Tx
	sql  string
	args []any
}

func (tx *recoveryQueryRecorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "WITH candidates AS MATERIALIZED") {
		tx.sql, tx.args = sql, append([]any(nil), args...)
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

// Bound executed placement/release work, independent of elapsed time, join
// algorithm or index choice. Stale bulk-import statistics used to turn the
// effective-placement LEFT JOIN into a Cartesian scan for every candidate.
func TestRecoveryReadsBoundPlacementWorkWithStaleStatistics(t *testing.T) {
	const unrelated = 501
	f, ids := newRecoveryPopulationFixture(t, unrelated)
	for _, mode := range []string{"unplaced", "later", "counts"} {
		t.Run(mode, func(t *testing.T) {
			err := f.store.read(t.Context(), f.person, f.project, func(ctx context.Context, tx pgx.Tx) error {
				recorded := &recoveryQueryRecorder{Tx: tx}
				if mode == "counts" {
					counts, err := recoveryCounts(ctx, recorded, f.person, f.releaseRow(t, f.release))
					if err != nil {
						return err
					}
					if counts.Unplaced != 2 || counts.Later != 2 || counts.Incomplete {
						t.Fatalf("recovery counts = %+v", counts)
					}
				} else {
					opt := ReadOptions{CompletedUnplaced: mode == "unplaced", CompletedLater: mode == "later"}
					release, want := "", []string{ids[1], ids[0]}
					if mode == "later" {
						release, want = f.release, []string{ids[3], ids[2]}
					}
					page, err := recoveryItems(ctx, recorded, f.person, f.project, release, opt, 200)
					if err != nil {
						return err
					}
					if page.Count != 2 || page.Incomplete || len(page.Items) != 2 || page.Items[0].ItemID != want[0] || page.Items[1].ItemID != want[1] {
						t.Fatalf("recovery page = %+v", page)
					}
				}
				if recorded.sql == "" {
					t.Fatal("no executed recovery count query captured")
				}
				var raw []byte
				if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON, TIMING OFF) "+recorded.sql, recorded.args...).Scan(&raw); err != nil {
					return err
				}
				var plan any
				if err := json.Unmarshal(raw, &plan); err != nil {
					return err
				}
				visits := recoveryPlacementVisits(plan)
				t.Logf("executed placement/release visits: %.0f", visits)
				if visits <= 0 || visits > float64(16*(unrelated+len(ids))) {
					t.Fatalf("placement/release visits = %.0f; want bounded work over %d items", visits, unrelated+len(ids))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func recoveryPlacementVisits(plan any) float64 {
	var visits float64
	switch node := plan.(type) {
	case []any:
		for _, child := range node {
			visits += recoveryPlacementVisits(child)
		}
	case map[string]any:
		if node["Relation Name"] == "ships_in" || node["Relation Name"] == "project_releases" {
			rows, _ := node["Actual Rows"].(float64)
			removed, _ := node["Rows Removed by Filter"].(float64)
			loops, _ := node["Actual Loops"].(float64)
			visits += (rows + removed) * loops
		}
		visits += recoveryPlacementVisits(node["Plan"])
		visits += recoveryPlacementVisits(node["Plans"])
	}
	return visits
}
