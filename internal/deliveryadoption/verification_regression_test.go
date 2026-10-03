// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func adoptionProjectReader(t *testing.T, f *fixture) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Original project reader') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, p.TenantID, p.ID, f.project)
		return err
	})
	return p
}

func adoptionMove(t *testing.T, f *fixture, mux *http.ServeMux, route, root, target string) int64 {
	t.Helper()
	path, body, typ := "move", `{"parent_id":"`+target+`"}`, "node.moved"
	if route == "project-move" {
		path, body, typ = route, `{"project_id":"`+target+`"}`, "node.project_moved"
	}
	adoptionMutationRequest(t, mux, f.p, "/api/nodes/"+root+"/"+path, body, http.StatusOK)
	var id int64
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM events WHERE node_id=$1 AND type=$2 AND undo_of IS NULL ORDER BY id DESC LIMIT 1`, root, typ).Scan(&id)
	})
	return id
}

func requireAdoptionVerification(t *testing.T, f *fixture, p tenant.Principal, members, missing int) {
	t.Helper()
	v, err := f.s.Verify(t.Context(), p, f.project)
	if err != nil || v.OK != (missing == 0) || v.Mode != "releases" || v.JourneyMembers != members || v.AdoptedMembers != members-missing || v.MissingArchiveMembers != missing || v.Incomplete || v.PostAdoptionJourneyEvents != 0 {
		t.Fatalf("adoption verification: %+v, %v; want %d members, %d missing", v, err, members, missing)
	}
}

func TestVerificationUsesJobAuthorityForProjectScopedMoveAndUndo(t *testing.T) {
	for _, route := range []string{"project-move", "move", "subtree"} {
		for _, undo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/undo=%t", route, undo), func(t *testing.T) {
				f := newFixture(t)
				target := f.node(t, "project", "DST-1", "", "open")
				release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
				member := f.member(t, f.project, release, "TK-1", 1)
				peer := f.member(t, f.project, release, "TK-2", 2)
				root := member
				if route == "subtree" {
					root = f.node(t, "epic", "EP-1", f.project, "open")
					f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, root)
				}
				reader := adoptionProjectReader(t, f)
				j := f.prepare(t, f.project)
				if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
					t.Fatal(err)
				}
				store := delivery.NewStore(f.d.App)
				if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
					t.Fatal(err)
				}
				mux := http.NewServeMux()
				nodes.New(f.d.App, nil).Mount(mux)
				events.New(f.d.App, events.WithUndoHandlers(nodes.UndoHandlers())).Mount(mux)
				id := adoptionMove(t, f, mux, route, root, target)
				if undo {
					adoptionMutationRequest(t, mux, f.p, fmt.Sprintf("/api/events/%d/undo", id), "", http.StatusCreated)
				}
				if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 {
					t.Fatal("move/Undo did not remove the ranked backlog placement")
				}
				// Even after Undo returns the node, the destination references
				// keep the original event hidden from this project-only reader.
				var visibleEvents, visibleTarget int
				checkVisibility := func() {
					t.Helper()
					if err := f.s.snapshot(t.Context(), reader, func(tx pgx.Tx) error {
						return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events WHERE id=$1 OR undo_of=$1),(SELECT count(*) FROM nodes WHERE id=$2)`, id, target).Scan(&visibleEvents, &visibleTarget)
					}); err != nil || visibleEvents != 0 || visibleTarget != 0 {
						t.Fatalf("fixture exposed destination or mutation events: %d, %d, %v", visibleEvents, visibleTarget, err)
					}
				}
				checkVisibility()
				requireAdoptionVerification(t, f, reader, 2, 0)
				checkVisibility()
				f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, peer)
				requireAdoptionVerification(t, f, reader, 2, 1)
				if undo {
					if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 0}}); err != nil {
						t.Fatal(err)
					}
					f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
					requireAdoptionVerification(t, f, reader, 2, 2)
				}
			})
		}
	}
}

func TestVerificationRecordsUndoRemovalOfPlacementCreatedAfterMove(t *testing.T) {
	for _, route := range []string{"project-move", "move", "subtree"} {
		t.Run(route, func(t *testing.T) {
			f := newFixture(t)
			first := f.node(t, "project", "DST-1", "", "open")
			destination := f.node(t, "project", "END-1", "", "open")
			release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
			member := f.member(t, f.project, release, "TK-1", 1)
			peer := f.member(t, f.project, release, "TK-2", 2)
			root := member
			if route == "subtree" {
				root = f.node(t, "epic", "EP-1", f.project, "open")
				f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, root)
			}
			reader := adoptionProjectReader(t, f)
			for _, project := range []string{f.project, destination} {
				j := f.prepare(t, project)
				if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
					t.Fatal(err)
				}
			}
			store := delivery.NewStore(f.d.App)
			if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			nodes.New(f.d.App, nil).Mount(mux)
			events.New(f.d.App, events.WithUndoHandlers(nodes.UndoHandlers())).Mount(mux)
			firstID := adoptionMove(t, f, mux, route, root, first)
			adoptionMutationRequest(t, mux, f.p, fmt.Sprintf("/api/events/%d/undo", firstID), "", http.StatusCreated)
			if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 {
				t.Fatal("first Undo did not return member to the unranked tail")
			}
			secondID := adoptionMove(t, f, mux, route, root, destination)
			if f.count(t, `SELECT jsonb_array_length(coalesce(before->'ships_in_before_move','[]')) FROM events WHERE id=$1`, secondID) != 0 {
				t.Fatal("second move unexpectedly had a placement snapshot")
			}
			if _, err := store.Place(t.Context(), f.p, destination, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: destination, ExpectedRevision: 0}}); err != nil {
				t.Fatal(err)
			}
			if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1 AND project_node_id=$2 AND release_node_id IS NULL AND revision=1`, member, destination) != 1 {
				t.Fatal("destination did not acquire a real ranked backlog placement")
			}
			adoptionMutationRequest(t, mux, f.p, fmt.Sprintf("/api/events/%d/undo", secondID), "", http.StatusCreated)
			if f.count(t, `SELECT count(*) FROM nodes WHERE id=$1 AND project_id=$2`, member, f.project) != 1 || f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 {
				t.Fatal("second Undo did not remove destination rank and return to original tail")
			}
			requireAdoptionVerification(t, f, f.p, 2, 0)
			requireAdoptionVerification(t, f, reader, 2, 0)
			if f.count(t, `SELECT count(*) FROM events WHERE undo_of=$1 AND before->'ships_in_before_move' @> jsonb_build_array(jsonb_build_object('item_id',$2::text,'project_id',$3::text,'revision',1))`, secondID, member, destination) != 1 {
				t.Fatal("Undo event omitted its actual placement removal")
			}
			f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, peer)
			requireAdoptionVerification(t, f, reader, 2, 1)
			if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 0}}); err != nil {
				t.Fatal(err)
			}
			f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
			requireAdoptionVerification(t, f, reader, 2, 2)
		})
	}
}

func TestAdoptionVerificationHTTPUsesActualService(t *testing.T) {
	f := newFixture(t)
	release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	f.member(t, f.project, release, "TK-1", 1)
	mux := http.NewServeMux()
	releases.New(f.d.App, releases.WithAdoptionReporting(f.s)).Mount(mux)
	verify := func(mode string, members int) {
		t.Helper()
		before := f.count(t, `SELECT count(*) FROM events`)
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+f.project+"/delivery/verify", nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), f.p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("verification route: %d %s", rec.Code, rec.Body.String())
		}
		var got Verification
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK || got.Mode != mode || got.JourneyMembers != members || got.AdoptedMembers != members || got.Incomplete || got.MissingArchiveMembers != 0 {
			t.Fatalf("verification route evidence: %+v", got)
		}
		if after := f.count(t, `SELECT count(*) FROM events`); after != before {
			t.Fatal("verification wrote events")
		}
	}
	verify("journey", 0)
	j := f.prepare(t, f.project)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	verify("releases", 1)
}
