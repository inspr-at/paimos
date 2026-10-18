// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
)

func (f *harnessFixture) expectNodeProgress(t *testing.T, id string, want int) eta.View {
	t.Helper()
	var view eta.View
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var err error
		view, err = eta.One(t.Context(), tx, id)
		return err
	})
	if view.Progress == nil || *view.Progress != want {
		t.Fatalf("node %s ETA %+v, want progress %d", id, view, want)
	}
	return view
}

// AEON-475: stopping a finished child must not lower its parent's progress.
// Unified work counts each leaf once, including unreported leaves at zero.
// Completed workers' ETA timestamps remain absent while progress contributes 100.
func TestEpicProgressKeepsFinishedChildrenAndNestedAverages(t *testing.T) {
	f := fixture(t)
	outer, inner, done, running, zero, unknown := uid(), uid(), uid(), uid(), uid(), uid()
	f.addNode(t, outer, "EPIC-1", "work", f.project, "Outer")
	f.addNode(t, inner, "EPIC-2", "work", outer, "Inner")
	f.addNode(t, done, "EPIC-3", "work", inner, "Finishing")
	f.addNode(t, running, "EPIC-4", "work", inner, "Running")
	f.addNode(t, zero, "EPIC-5", "work", outer, "Zero")
	f.addNode(t, unknown, "EPIC-6", "work", inner, "Unknown")
	doneLease, runningLease, zeroLease := "epic-done-lease-0000000000000001", "epic-running-lease-00000000000001", "epic-zero-lease-0000000000000001"
	doneSession := f.registerSession(t, f.agent.ID, "worker", done, "epic-done-ref-000000000000001", doneLease)
	runningSession := f.registerSession(t, f.agent.ID, "worker", running, "epic-running-ref-000000000001", runningLease)
	zeroSession := f.registerSession(t, f.agent.ID, "worker", zero, "epic-zero-ref-000000000000001", zeroLease)
	ready := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	f.beat(t, doneSession, doneLease, 1, map[string]any{"progress_pct": 100, "eta_ready_at": ready.Add(time.Hour).Format(time.RFC3339)})
	f.beat(t, runningSession, runningLease, 1, map[string]any{"progress_pct": 40, "eta_ready_at": ready.Format(time.RFC3339)})
	f.beat(t, zeroSession, zeroLease, 1, map[string]any{"progress_pct": 0})
	f.expectNodeProgress(t, inner, 47)
	f.expectNodeProgress(t, outer, 35)
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+doneSession+"/stop", map[string]string{"reason": "process_exited"}, doneLease), 200)
	view := f.expectNodeProgress(t, inner, 47)
	if view.ReadyAt == nil || !view.ReadyAt.Equal(ready) || view.Finished {
		t.Fatalf("epic must retain only the running ETA and remain unfinished: %+v", view)
	}
	f.expectNodeProgress(t, outer, 35)
	finished := f.expectNodeProgress(t, done, 100)
	if !finished.Finished || finished.FinishedAt == nil || finished.ReadyAt != nil || finished.LiveAt != nil {
		t.Fatalf("completed child keeps progress without an estimate: %+v", finished)
	}
	// Previous binaries keep the published signature and receive the same
	// unified leaf projection, including completion and unreported leaves.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var legacy int
		if err := tx.QueryRow(t.Context(), `SELECT progress_pct FROM aeon_node_eta($1::uuid)`, inner).Scan(&legacy); err != nil {
			return err
		}
		if legacy != 47 {
			t.Fatalf("published ETA function disagrees: progress %d, want 47", legacy)
		}
		loaded, err := eta.Load(t.Context(), tx, []string{inner, outer, done})
		if err != nil {
			return err
		}
		for id, want := range map[string]int{inner: 47, outer: 35, done: 100} {
			if got := loaded[id]; got.Progress == nil || *got.Progress != want {
				t.Fatalf("batch ETA for %s: %+v, want %d", id, got, want)
			}
		}
		return nil
	})
	// The completion function must not reveal another tenant's worker evidence.
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM aeon_node_completion($1::uuid)`, done).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("foreign tenant sees %d completion rows", count)
		}
		return nil
	})

	// A restarted child uses its current report, and a later failed worker
	// supersedes the earlier clean exit instead of reviving that old 100.
	restartLease := "epic-restart-lease-0000000000001"
	restarted := f.registerSession(t, f.agent.ID, "worker", done, "epic-restart-ref-000000000001", restartLease)
	f.beat(t, restarted, restartLease, 1, map[string]any{"progress_pct": 20})
	f.expectNodeProgress(t, inner, 20)
	f.expectNodeProgress(t, outer, 15)
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+restarted+"/stop", map[string]string{"reason": "process_failed"}, restartLease), 200)
	f.expectNodeProgress(t, inner, 13)
	f.expectNodeProgress(t, outer, 10)
}

func TestEpicProgressRequiresCurrentUnarchivedCleanCompletion(t *testing.T) {
	f := fixture(t)
	epic, running := uid(), uid()
	f.addNode(t, epic, "PROOF-1", "work", f.project, "Completion evidence")
	f.addNode(t, running, "PROOF-2", "work", epic, "Running")
	lease := "epic-proof-running-lease-0000001"
	session := f.registerSession(t, f.agent.ID, "worker", running, "epic-proof-running-ref-000001", lease)
	f.beat(t, session, lease, 1, map[string]any{"progress_pct": 40})
	for i, tc := range []struct {
		name     string
		progress any
		reason   any
		archived bool
		deleted  bool
		openRole string
	}{
		{name: "crashed at 100", progress: 100, reason: "process_failed"},
		{name: "stopped at 100", progress: 100, reason: "stopped"},
		{name: "lost contact at 100", progress: 100, reason: "heartbeat_lost"},
		{name: "missing exit reason", progress: 100},
		{name: "incomplete clean exit", progress: 99, reason: "process_exited"},
		{name: "unreported clean exit", reason: "process_exited"},
		{name: "archived completion", progress: 100, reason: "process_exited", archived: true},
		{name: "deleted child", progress: 100, reason: "process_exited", deleted: true},
		{name: "open worker without progress", progress: 100, reason: "process_exited", openRole: "worker"},
		{name: "open coordinator without progress", progress: 100, reason: "process_exited", openRole: "coordinator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := uid()
			f.addNode(t, child, fmt.Sprintf("PROOF-%d", i+3), "work", epic, tc.name)
			defer func() {
				// Keep each case's child out of the next case's average, even if
				// a fixture assertion fails before it reaches its stopped state.
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, child, f.project)
					return err
				})
			}()
			lease := fmt.Sprintf("epic-proof-lease-%022d", i)
			session := f.registerSession(t, f.agent.ID, "worker", child, fmt.Sprintf("epic-proof-ref-%016d", i), lease)
			f.beat(t, session, lease, 1, map[string]any{"progress_pct": tc.progress})
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped', stopped_at=clock_timestamp(), stop_reason=$2,
					archived_at=CASE WHEN $3 THEN clock_timestamp() END,
					recovery_process_state=CASE WHEN $3 THEN 'unknown' END,
					recovery_request_id=CASE WHEN $3 THEN gen_random_uuid() END,
					recovery_request_digest=CASE WHEN $3 THEN 'fixture'::bytea END,
					recovery_actor_id=CASE WHEN $3 THEN agent_principal_id END,
					recovery_reason=CASE WHEN $3 THEN 'fixture' END WHERE id=$1`, session, tc.reason, tc.archived)
				return err
			})
			if tc.deleted {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, child)
					return err
				})
			}
			if tc.openRole != "" {
				f.registerSession(t, f.agent.ID, tc.openRole, child, fmt.Sprintf("epic-open-ref-%016d", i), fmt.Sprintf("epic-open-lease-%022d", i))
			}
			want := 20 // The second, unreported leaf contributes zero.
			if tc.deleted {
				want = 40 // A deleted leaf is absent from the scope.
			}
			f.expectNodeProgress(t, epic, want)
		})
	}
}

// This exercises the real paginated list: sorting must happen on the same
// recursive completion-aware percent that eta.Load puts on each returned row.
func TestEpicProgressSortMatchesDisplayedAverage(t *testing.T) {
	f := fixture(t)
	group := uid()
	f.addNode(t, group, "SCOPE-1", "guideline", f.project, "Parent sort scope")
	n := 0
	childAt := func(parent string, progress int, stop string) {
		t.Helper()
		n++
		child := uid()
		f.addNode(t, child, fmt.Sprintf("CHILD-%d", n), "work", parent, "Child")
		lease := fmt.Sprintf("epic-sort-lease-%022d", n)
		session := f.registerSession(t, f.agent.ID, "worker", child, fmt.Sprintf("epic-sort-ref-%016d", n), lease)
		f.beat(t, session, lease, 1, map[string]any{"progress_pct": progress})
		if stop != "" {
			expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/stop", map[string]string{"reason": stop}, lease), 200)
		}
	}
	for _, key := range []string{"ORDER-1", "ORDER-2", "ORDER-3", "ORDER-4"} {
		epic := uid()
		f.addNode(t, epic, key, "work", group, key)
		switch key {
		case "ORDER-1":
			childAt(epic, 100, "process_exited")
			childAt(epic, 40, "") // 70, though the legacy projection was 40.
		case "ORDER-2":
			childAt(epic, 60, "")
		case "ORDER-3":
			childAt(epic, 100, "process_exited")
		case "ORDER-4":
			childAt(epic, 100, "process_failed") // Unreported leaf contributes zero.
		}
	}
	mux := http.NewServeMux()
	nodes.New(f.db.App, nil).Mount(mux)
	for _, tc := range []struct {
		sort string
		want []string
	}{
		{"progress", []string{"ORDER-4", "ORDER-2", "ORDER-1", "ORDER-3"}},
		{"-progress", []string{"ORDER-3", "ORDER-1", "ORDER-2", "ORDER-4"}},
	} {
		t.Run(tc.sort, func(t *testing.T) {
			for _, limit := range []int{100, 2} {
				// Parent filtering keeps leaf rows out without relying on retired kinds.
				r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/nodes?parent_id=%s&kind=work&sort=%s&limit=%d", group, tc.sort, limit), nil)
				r = r.WithContext(tenant.WithPrincipal(r.Context(), f.person))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				expect(t, w, 200)
				var page struct {
					Items []struct {
						Key string    `json:"key"`
						ETA *eta.View `json:"eta"`
					} `json:"items"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				var keys []string
				for _, item := range page.Items {
					keys = append(keys, item.Key)
					if want, known := map[string]int{"ORDER-1": 70, "ORDER-2": 60, "ORDER-3": 100}[item.Key]; known {
						if item.ETA == nil || item.ETA.Progress == nil || *item.ETA.Progress != want || item.ETA.Finished {
							t.Fatalf("%s shows %+v, want unfinished epic at %d", item.Key, item.ETA, want)
						}
					} else if item.ETA == nil || item.ETA.Progress == nil || *item.ETA.Progress != 0 || item.ETA.Finished || item.ETA.ReadyAt != nil || item.ETA.LiveAt != nil {
						t.Fatalf("failed child must contribute zero without completion or ETA: %+v", item.ETA)
					}
				}
				want := tc.want[:min(limit, len(tc.want))]
				if !slices.Equal(keys, want) {
					t.Fatalf("limit=%d sort=%s: %v, want %v", limit, tc.sort, keys, want)
				}
			}
		})
	}
}
