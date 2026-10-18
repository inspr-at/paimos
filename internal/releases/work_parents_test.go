// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkParentPlacementRechecksRevokedGrantUnderFence(t *testing.T) {
	f := ticketSetup(t)
	f.existing("work", f.feature, "Leaf", "open")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	held, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.Background()) }()
	// Access changes follow tenant -> tree. Holding pairing first would make
	// the test's revocation itself deadlock with a correctly fenced request.
	if err = authz.LockProjectMutation(ctx, held, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err = held.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	ready := make(chan uint32, 1)
	cfg := f.db.App.Config()
	cfg.ConnConfig.Tracer = membershipFenceTrace{ready: ready}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); pool.Close() })
	mux := http.NewServeMux()
	New(pool).Mount(mux)
	result := make(chan *httptest.ResponseRecorder, 1)
	raw, _ := json.Marshal(membershipInput{Revision: 1, IDs: []string{f.feature}})
	go func() {
		req := httptest.NewRequest(http.MethodPost, f.membershipPath(), strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		result <- w
	}()
	completed := false
	defer func() {
		cancel()
		_ = held.Rollback(context.Background())
		if !completed {
			<-result
		}
	}()
	workerPID := dbtest.Await(t, ctx, ready)
	if err = dbtest.WaitForBlocked(ctx, f.db.Admin, int(workerPID), holderPID, membershipTenantFence); err != nil {
		t.Fatal(err)
	}
	if _, err = held.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.person.TenantID, f.person.ID); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		completed = true
		if w.Code != 403 || !strings.Contains(w.Body.String(), "project access required") {
			t.Fatalf("wrong rejection after revocation: %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var count int
	if err = f.db.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM journey_tickets WHERE tenant_id=$1)+(SELECT count(*) FROM work_parent_releases WHERE tenant_id=$1)`, f.person.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked placement left writes: count=%d err=%v", count, err)
	}
}

func TestWorkParentPlacementBoundAndWorkEdges(t *testing.T) {
	f := ticketSetup(t)
	leaf := f.existing("work", f.feature, "Leaf", "open")
	memory := f.existing("memory", f.feature, "Notes", "open")
	hiddenUnderMemory := f.existing("work", memory, "Separate work", "open")
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	if len(out.LeafIDs) != 1 || out.LeafIDs[0] != leaf {
		t.Fatalf("non-work edge changed scope: %v", out.LeafIDs)
	}
	if row := f.readMembership(hiddenUnderMemory)[0]; row.ReleaseCount != 0 {
		t.Fatalf("placement crossed non-work edge: %+v", row)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,'Wide leaf '||i FROM node_kinds k CROSS JOIN generate_series(1,1000) i WHERE k.slug='work'`, f.person.TenantID, f.feature)
		return err
	})
	// Each inherited create changed the release revision. Obtain the real fence
	// so the assertion cannot pass merely because the request was stale.
	var rev int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT revision FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&rev)
	})
	raw, _ := json.Marshal(membershipInput{Revision: rev, IDs: []string{f.feature}})
	w := f.request(f.person, http.MethodPost, f.membershipPath(), string(raw))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "exceeds 1000") {
		t.Fatalf("wrong expansion failure: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkParentPlacementAndFutureLeaves(t *testing.T) {
	f := ticketSetup(t)
	nested := f.existing("work", f.feature, "Nested parent", "open")
	a := f.existing("work", nested, "First leaf", "open")
	b := f.existing("work", f.feature, "Second leaf", "open")
	f.existing("memory", f.feature, "Non-work child", "open")
	out := membershipOK(t, f.addExisting([]string{f.feature, a}, 1, false))
	if len(out.Walker.Tickets) != 2 {
		t.Fatalf("leaves counted: %+v", out.Walker.Tickets)
	}
	rows := f.readMembership(f.feature, nested, a, b)
	for _, row := range rows {
		if row.ReleaseCount != 1 || row.ReleaseID == nil || *row.ReleaseID != f.release {
			t.Fatalf("ships in: %+v", row)
		}
	}
	if !rows[0].IsParent || !rows[1].IsParent || rows[2].IsParent {
		t.Fatalf("shape: %+v", rows)
	}
	fresh := f.existing("work", nested, "New leaf", "open")
	got := f.readMembership(fresh)[0]
	if got.ReleaseID == nil || *got.ReleaseID != f.release {
		t.Fatalf("new leaf did not inherit: %+v", got)
	}
	f.tx(func(tx pgx.Tx) error {
		var count, parents int
		err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE ticket_node_id=ANY($2::uuid[])) FROM journey_tickets WHERE release_node_id=$1`, f.release, []string{f.feature, nested}).Scan(&count, &parents)
		if count != 3 || parents != 0 {
			t.Errorf("scope count=%d parents=%d", count, parents)
		}
		return err
	})
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 409 {
		t.Fatalf("stale undo after new leaf: %d %s", w.Code, w.Body.String())
	}
}

func TestInheritedLeafBecomingParentReconcilesLiveMembership(t *testing.T) {
	f := ticketSetup(t)
	former := f.existing("work", f.feature, "Former leaf", "open")
	membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	child := f.existing("work", former, "Current leaf", "open")
	f.tx(func(tx pgx.Tx) error {
		var parents int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=$1`, former).Scan(&parents); err != nil {
			return err
		}
		if parents != 0 {
			t.Fatalf("former leaf remains a live member: %d", parents)
		}
		var intent string
		if err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM work_parent_releases WHERE parent_node_id=$1`, former).Scan(&intent); err != nil {
			return err
		}
		if intent != f.release {
			t.Fatalf("former leaf lost future placement: %s", intent)
		}
		for _, state := range []string{"planning", "building"} {
			if _, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state=$2 WHERE release_node_id=$1`, f.release, state); err != nil {
				return err
			}
			walker, err := load(t.Context(), tx, f.project, f.release)
			if err != nil {
				return err
			}
			if len(walker.Tickets) != 1 || walker.Tickets[0].NodeID != child {
				t.Fatalf("%s walker counts former leaf: %+v", state, walker.Tickets)
			}
			var captured []byte
			if err := tx.QueryRow(t.Context(), `SELECT aeon_release_note_snapshot($1,$2)`, f.project, f.release).Scan(&captured); err != nil {
				return err
			}
			var snapshot struct {
				Tickets []struct {
					ID string `json:"id"`
				} `json:"tickets"`
			}
			if err := json.Unmarshal(captured, &snapshot); err != nil {
				return err
			}
			if len(snapshot.Tickets) != 1 || snapshot.Tickets[0].ID != child {
				t.Fatalf("%s capture counts former leaf: %s", state, captured)
			}
		}
		return nil
	})
}

func TestPlacedLeafBecomingParentPreservesFrozenSnapshot(t *testing.T) {
	f := ticketSetup(t)
	// Direct leaf placement also establishes future intent when children arrive.
	membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	child := f.existing("work", f.feature, "Inherited child", "open")
	if row := f.readMembership(child)[0]; row.ReleaseID == nil || *row.ReleaseID != f.release {
		t.Fatalf("direct placement was lost on becoming a parent: %+v", row)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=$1`, f.release)
		return err
	})
	path := "/api/projects/" + f.project + "/releases/" + f.release + "/note-snapshot"
	frozen := f.request(f.person, http.MethodGet, path, "")
	if frozen.Code != 200 || !strings.Contains(frozen.Body.String(), child) || !strings.Contains(frozen.Body.String(), `"frozen":true`) {
		t.Fatalf("expected frozen child snapshot: %d %s", frozen.Code, frozen.Body.String())
	}
	fresh := f.existing("work", child, "After publication", "open")
	if row := f.readMembership(fresh)[0]; row.ReleaseID != nil || row.InheritanceNote == nil || *row.InheritanceNote != "parent_release_closed" {
		t.Fatalf("closed direct leaf placement not inherited as backlog: %+v", row)
	}
	after := f.request(f.person, http.MethodGet, path, "")
	if after.Code != 200 || after.Body.String() != frozen.Body.String() {
		t.Fatal("parent transition changed frozen snapshot")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE release_node_id=$1 AND ticket_node_id=$2`, f.release, child).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("parent transition rewrote released membership evidence")
		}
		walker, err := load(t.Context(), tx, f.project, f.release)
		if err == nil && len(walker.Tickets) != 0 {
			t.Fatalf("live released walker includes a parent: %+v", walker.Tickets)
		}
		return err
	})
}

func TestLeafShapeChangesReconcileMovedAndRestoredChildren(t *testing.T) {
	f := ticketSetup(t)
	former := f.existing("work", f.feature, "Former leaf", "open")
	membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	child := f.existing("work", former, "Current leaf", "open")
	for _, change := range []struct {
		sql    string
		args   []any
		parent bool
	}{
		{`UPDATE nodes SET parent_id=$2 WHERE id=$1`, []any{child, f.project}, false},
		{`UPDATE nodes SET parent_id=$2 WHERE id=$1`, []any{child, former}, true},
		{`UPDATE nodes SET deleted_at=now() WHERE id=$1`, []any{child}, false},
		{`UPDATE nodes SET deleted_at=NULL WHERE id=$1`, []any{child}, true},
	} {
		f.tx(func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), change.sql, change.args...); err != nil {
				return err
			}
			var member bool
			if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM journey_tickets WHERE ticket_node_id=$1 AND release_node_id=$2)`, former, f.release).Scan(&member); err != nil {
				return err
			}
			if member == change.parent {
				t.Fatalf("parent=%v has live membership=%v after %s", change.parent, member, change.sql)
			}
			return nil
		})
	}
}

func TestFormerParentReassignmentUpdatesFutureIntentAndUndo(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run(fmt.Sprint("undo=", undo), func(t *testing.T) {
			f := ticketSetup(t)
			former := f.existing("work", f.feature, "Former leaf", "open")
			membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
			child := f.existing("work", former, "First child", "open")
			second := f.existing("release", f.project, "Later choice", "open")
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, f.person.TenantID, second, f.project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, second)
				return err
			})
			out := membershipOK(t, f.request(f.person, http.MethodPost, "/api/projects/"+f.project+"/releases/"+second+"/membership", fmt.Sprintf(`{"expected_revision":1,"ticket_node_ids":[%q],"confirm_move":true}`, former)))
			want := second
			if undo {
				events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
				if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 201 {
					t.Fatalf("undo reassignment: %d %s", w.Code, w.Body.String())
				}
				want = f.release
			}
			f.tx(func(tx pgx.Tx) error {
				var actual string
				if err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM work_parent_releases WHERE parent_node_id=$1`, former).Scan(&actual); err != nil {
					return err
				}
				if actual != want {
					t.Errorf("stale intent: got %s want %s", actual, want)
				}
				// Closing the obsolete choice must not divert new descendants.
				if !undo {
					_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=$1`, f.release)
					return err
				}
				return nil
			})
			fresh := f.existing("work", former, "New child after choice", "open")
			row := f.readMembership(fresh)[0]
			if row.ReleaseID == nil || *row.ReleaseID != want || row.InheritanceNote != nil {
				t.Fatalf("child inherited obsolete choice: %+v want %s", row, want)
			}
		})
	}
}

func TestWorkParentPlacementThousandLeavesWithNestedParents(t *testing.T) {
	f := ticketSetup(t)
	nested := f.existing("work", f.feature, "Intermediate parent", "open")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,'Boundary leaf '||i FROM node_kinds k CROSS JOIN generate_series(1,1000) i WHERE k.slug='work'`, f.person.TenantID, nested)
		return err
	})
	out := membershipOK(t, f.addExisting([]string{f.feature, nested}, 1, false))
	if len(out.LeafIDs) != 1000 || len(out.Walker.Tickets) != 1000 {
		t.Fatalf("exact boundary omitted leaves: ids=%d tickets=%d", len(out.LeafIDs), len(out.Walker.Tickets))
	}
}

func TestWorkParentWalkerKeepsScreenLinksBoundToEachLeaf(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("work", f.feature, "First leaf", "open")
	b := f.existing("work", f.feature, "Second leaf", "open")
	c := f.existing("work", f.feature, "Unlinked leaf", "open")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'screen','Screen','SCR','screen')`, f.person.TenantID)
		return err
	})
	screen := f.existing("screen", f.project, "Shared screen", "open")
	other := f.existing("screen", f.project, "Second screen", "open")
	deleted := f.existing("screen", f.project, "Deleted screen", "open")
	memory := f.existing("memory", f.project, "Non-screen", "open")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type)
 VALUES($1,$2,$5,'implements'),($1,$5,$2,'cites'),($1,$3,$5,'implements'),($1,$6,$3,'implements'),($1,$2,$7,'implements'),($1,$4,$8,'implements')`, f.person.TenantID, a, b, c, screen, other, memory, deleted); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted)
		return err
	})
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	bScreens := []string{screen, other}
	sort.Strings(bScreens)
	want := map[string][]string{a: {screen}, b: bScreens, c: {}}
	if len(out.Walker.Tickets) != len(want) {
		t.Fatalf("walker lost leaves: %+v", out.Walker.Tickets)
	}
	for _, ticket := range out.Walker.Tickets {
		expected, ok := want[ticket.NodeID]
		if !ok || !slices.Equal(ticket.ScreenIDs, expected) {
			t.Fatalf("screen links crossed leaves or included deleted/non-screen nodes: %+v want %v", ticket, expected)
		}
	}
}

func TestFormerParentPlanRemovalAndUndoRespectsBacklog(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run(fmt.Sprint("undo=", undo), func(t *testing.T) {
			f := ticketSetup(t)
			former := f.existing("work", f.feature, "Former parent", "open")
			membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
			child := f.existing("work", former, "Temporary child", "open")
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child)
				return err
			})
			var rev int64
			f.tx(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT revision FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&rev)
			})
			w := f.request(f.person, http.MethodPut, "/api/projects/"+f.project+"/releases/"+f.release+"/plan", fmt.Sprintf(`{"expected_revision":%d,"ordered_ticket_ids":[%q],"included_ticket_ids":[]}`, rev, former))
			if w.Code != 200 {
				t.Fatalf("remove former parent: %d %s", w.Code, w.Body.String())
			}
			var eventID int64
			f.tx(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='journey.release_membership_changed' ORDER BY id DESC LIMIT 1`).Scan(&eventID)
			})
			if undo {
				events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
				if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", eventID), ""); w.Code != 201 {
					t.Fatalf("undo removal: %d %s", w.Code, w.Body.String())
				}
			}
			f.tx(func(tx pgx.Tx) error {
				var intent *string
				if err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM work_parent_releases WHERE parent_node_id=$1`, former).Scan(&intent); err != nil {
					return err
				}
				if undo && (intent == nil || *intent != f.release) || !undo && intent != nil {
					t.Errorf("removal/Undo did not reconcile intent: %v undo=%v", intent, undo)
				}
				return nil
			})
			fresh := f.existing("work", former, "After removal", "open")
			row := f.readMembership(fresh)[0]
			if undo && (row.ReleaseID == nil || *row.ReleaseID != f.release) || !undo && row.ReleaseID != nil {
				t.Fatalf("removal/Undo inherited old placement: %+v undo=%v", row, undo)
			}
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, fresh)
				return err
			})
			f.tx(func(tx pgx.Tx) error {
				var assigned *string
				if err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM journey_tickets WHERE ticket_node_id=$1`, former).Scan(&assigned); err != nil {
					return err
				}
				if undo && (assigned == nil || *assigned != f.release) || !undo && assigned != nil {
					t.Errorf("restored leaf ignored explicit backlog/Undo: %v undo=%v", assigned, undo)
				}
				return nil
			})
		})
	}
}

func TestParentPlacementUndoPreservesRetainedBacklogProjection(t *testing.T) {
	f := ticketSetup(t)
	former := f.existing("work", f.feature, "Estimated former parent", "open")
	membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET feature_node_id=$2,source='requirements',estimated_hours=7.25,access_change=true,scope_revision_required=false,walker_position=42 WHERE ticket_node_id=$1`, former, f.feature)
		return err
	})
	child := f.existing("work", former, "Temporary child", "open")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET release_node_id=NULL WHERE ticket_node_id=$1`, former)
		return err
	})
	var backlog []byte
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&backlog)
	})
	fresh := f.existing("work", former, "Unassigned child", "open")
	second := f.existing("release", f.project, "Temporary choice", "open")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, f.person.TenantID, second, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, second)
		return err
	})
	out := membershipOK(t, f.request(f.person, http.MethodPost, "/api/projects/"+f.project+"/releases/"+second+"/membership", fmt.Sprintf(`{"expected_revision":1,"ticket_node_ids":[%q]}`, former)))
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 201 {
		t.Fatalf("undo temporary parent placement: %d %s", w.Code, w.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var intent *string
		if err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM work_parent_releases WHERE parent_node_id=$1`, former).Scan(&intent); err != nil {
			if err == pgx.ErrNoRows {
				t.Fatal("Undo erased the retained backlog projection")
			}
			return err
		}
		if intent != nil {
			t.Error("Undo lost explicit backlog intent")
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, fresh); err != nil {
			return err
		}
		var restored []byte
		if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&restored); err != nil {
			return err
		}
		if string(restored) != string(backlog) {
			t.Fatalf("parent-placement Undo lost retained backlog metadata: before=%s after=%s", backlog, restored)
		}
		return nil
	})
}

func TestParentReleaseSummaryReportsPartialCoverage(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("work", f.feature, "Assigned", "open")
	b := f.existing("work", f.feature, "Backlog", "open")
	membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET release_node_id=NULL WHERE ticket_node_id=$1`, b)
		return err
	})
	rows := f.readMembership(f.feature, a, b)
	if rows[0].ReleaseCount != 1 || len(rows[0].LeafIDs) != 2 || rows[0].AssignedLeaves != 1 || rows[1].AssignedLeaves != 1 || rows[2].AssignedLeaves != 0 {
		t.Fatalf("partial assignment presented as full coverage: %+v", rows)
	}
}

func TestExistingPlanningParentRepairPreservesExplicitBacklog(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("work", f.feature, "Unprojected child", "open")
	b := f.existing("work", f.feature, "Explicit backlog", "open")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source)
 VALUES($1,$2,$4,$5,0,'manual'),($1,$3,$4,NULL,1,'manual')`, f.person.TenantID, f.feature, b, f.project, f.release)
		return err
	})
	sql, err := os.ReadFile("../db/migrations/1237_work_release_leaf_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(sql), "DO $$")
	if start < 0 {
		t.Fatal("repair block missing")
	}
	repair := string(sql)[start:]
	end := strings.Index(repair, "$$;")
	if end < 0 {
		t.Fatal("repair block end missing")
	}
	repair = repair[:end+3]
	if _, err := f.db.Admin.Exec(t.Context(), repair); err != nil {
		t.Fatal(err)
	}
	rows := f.readMembership(a, b, f.feature)
	if rows[0].ReleaseID == nil || *rows[0].ReleaseID != f.release || rows[1].ReleaseID != nil || rows[2].AssignedLeaves != 1 {
		t.Fatalf("repair lost placement or overrode explicit backlog: %+v", rows)
	}
	f.tx(func(tx pgx.Tx) error {
		var parents int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=$1`, f.feature).Scan(&parents)
		if err == nil && parents != 0 {
			t.Fatal("repair retained live parent membership")
		}
		return err
	})
	before := f.counts()
	if _, err := f.db.Admin.Exec(t.Context(), repair); err != nil {
		t.Fatal(err)
	}
	if f.counts() != before {
		t.Fatal("repair replay changed revisions or rows")
	}
}

func TestWorkParentPlacementUndoRestoresFutureLeafIntent(t *testing.T) {
	f := ticketSetup(t)
	leaf := f.existing("work", f.feature, "Leaf", "open")
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 201 {
		t.Fatalf("undo parent: %d %s", w.Code, w.Body.String())
	}
	fresh := f.existing("work", f.feature, "After undo", "open")
	for _, row := range f.readMembership(leaf, fresh, f.feature) {
		if row.ReleaseCount != 0 {
			t.Fatalf("undo left intent or membership: %+v", row)
		}
	}
}

func TestWorkParentFrozenReleaseLeavesScopeUnchanged(t *testing.T) {
	for _, state := range []string{"building", "candidate", "deploying", "access", "released", "superseded"} {
		t.Run(state, func(t *testing.T) {
			f := ticketSetup(t)
			original := f.existing("work", f.feature, "Original", "open")
			membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state=$2,released_at=CASE WHEN $2 IN ('released','superseded') THEN now() END WHERE release_node_id=$1`, f.release, state)
				return err
			})
			fresh := f.existing("work", f.feature, "Later leaf", "open")
			got := f.readMembership(fresh)[0]
			if got.ReleaseCount != 0 || got.InheritanceNote == nil || *got.InheritanceNote != "parent_release_closed" {
				t.Fatalf("backlog note: %+v", got)
			}
			f.tx(func(tx pgx.Tx) error {
				var nodeState string
				var count int
				if err := tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, fresh).Scan(&nodeState); err != nil {
					return err
				}
				if nodeState != "backlog" {
					t.Errorf("state=%s", nodeState)
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE release_node_id=$1 AND ticket_node_id=$2`, f.release, original).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					t.Error("original scope changed")
				}
				return nil
			})
		})
	}
}

func TestWorkParentSplitShipsInAndAtomicFrozenRejection(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("work", f.feature, "A", "open")
	b := f.existing("work", f.feature, "B", "open")
	second := f.existing("release", f.project, "Other marketing name", "open")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,2,'released',now())`, f.person.TenantID, second, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$4,$5,0,'manual'),($1,$3,$4,$6,1,'manual')`, f.person.TenantID, a, b, f.project, f.release, second)
		return err
	})
	row := f.readMembership(f.feature)[0]
	if !row.IsParent || row.ReleaseCount != 2 || row.ReleaseID != nil || len(row.Releases) != 2 {
		t.Fatalf("split summary: %+v", row)
	}
	w := f.addExisting([]string{f.feature}, 1, true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "released or active") {
		t.Fatalf("wrong frozen failure: %d %s", w.Code, w.Body.String())
	}
	row = f.readMembership(f.feature)[0]
	if row.ReleaseCount != 2 {
		t.Fatal("rejected placement changed leaves")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_parent_releases`).Scan(&count)
		if count != 0 {
			t.Error("rejected batch left intent")
		}
		return err
	})
}

// A selected parent may include its done descendants, but must not relax the
// closed-leaf rule for an unrelated directly selected leaf in the same batch.
func TestWorkParentPlacementDoesNotPermitUnrelatedClosedLeaf(t *testing.T) {
	f := ticketSetup(t)
	child := f.existing("work", f.feature, "Closed descendant", "done")
	other := f.existing("work", f.project, "Unrelated closed leaf", "done")
	w := f.addExisting([]string{f.feature, other}, 1, false)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "closed tickets") {
		t.Fatalf("wrong rejection: %d %s", w.Code, w.Body.String())
	}
	for _, row := range f.readMembership(f.feature, child, other) {
		if row.ReleaseCount != 0 {
			t.Fatalf("rejected batch wrote membership: %+v", row)
		}
	}
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	if len(out.Walker.Tickets) != 1 || out.Walker.Tickets[0].NodeID != child {
		t.Fatalf("parent placement missed its closed descendant: %+v", out.Walker.Tickets)
	}
}
