// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func projectOnlyVerificationFixture(t *testing.T, route string) (*fixture, tenant.Principal, tenant.Principal, string, string, string, string) {
	t.Helper()
	f := newFixture(t)
	destination := f.node(t, "project", "DST-1", "", "open")
	release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	member := f.member(t, f.project, release, "TK-1", 1)
	peer := f.member(t, f.project, release, "TK-2", 2)
	root := member
	if route == "subtree" {
		root = f.node(t, "epic", "EP-1", f.project, "open")
		f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, root)
	}
	// Discovery records authority for both jobs. Give this project executor a
	// temporary destination binding for its adoption, then revoke that binding
	// before adopting the source and checking any move/Undo evidence.
	executor := projectExecutor(t, f)
	f.sql(t, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='owner'`, executor.TenantID, executor.ID, destination)
	j := f.prepare(t, destination)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	f.sql(t, `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='project' AND scope_id=$2`, executor.ID, destination)
	reader := adoptionProjectReader(t, f)
	j = f.prepare(t, f.project)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM delivery_adoption_jobs WHERE project_node_id=$1 AND state='adopted' AND executing_principal_id=$2`, f.project, executor.ID) != 1 {
		t.Fatal("source job was not adopted by the project-only executor")
	}
	store := delivery.NewStore(f.d.App)
	if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
		t.Fatal(err)
	}
	return f, reader, executor, destination, member, peer, root
}

func requireHiddenVerificationEvidence(t *testing.T, f *fixture, p tenant.Principal, destination string, mutation int64) {
	t.Helper()
	var visibleNodes, visibleEvents, visiblePlacements int
	err := f.s.snapshot(t.Context(), p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT
		 (SELECT count(*) FROM nodes WHERE id=$1),
		 (SELECT count(*) FROM events WHERE id=$2 OR undo_of=$2),
		 (SELECT count(*) FROM ships_in WHERE project_node_id=$1)`, destination, mutation).Scan(&visibleNodes, &visibleEvents, &visiblePlacements)
	})
	if err != nil || visibleNodes != 0 || visibleEvents != 0 || visiblePlacements != 0 {
		t.Fatalf("destination evidence escaped project visibility: nodes=%d events=%d placements=%d err=%v", visibleNodes, visibleEvents, visiblePlacements, err)
	}
}

func TestProjectOnlyExecutorVerifiesMoveAndUndo(t *testing.T) {
	for _, route := range []string{"project-move", "move", "subtree"} {
		for _, undo := range []string{"none", "unranked", "destination-placement"} {
			t.Run(route+"/"+undo, func(t *testing.T) {
				f, reader, executor, destination, member, peer, root := projectOnlyVerificationFixture(t, route)
				mux := http.NewServeMux()
				nodes.New(f.d.App, nil).Mount(mux)
				events.New(f.d.App, events.WithUndoHandlers(nodes.UndoHandlers())).Mount(mux)
				id := adoptionMove(t, f, mux, route, root, destination)
				if undo == "destination-placement" {
					store := delivery.NewStore(f.d.App)
					if _, err := store.Place(t.Context(), f.p, destination, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: destination, ExpectedRevision: 0}}); err != nil {
						t.Fatal(err)
					}
				}
				if undo != "none" {
					adoptionMutationRequest(t, mux, f.p, fmt.Sprintf("/api/events/%d/undo", id), "", http.StatusCreated)
				}
				if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 {
					t.Fatal("move/Undo retained a ranked placement")
				}
				for _, p := range []tenant.Principal{reader, executor, f.p} {
					// The workspace caller must also use the bound job executor.
					requireAdoptionVerification(t, f, p, 2, 0)
				}
				for _, p := range []tenant.Principal{reader, executor} {
					requireHiddenVerificationEvidence(t, f, p, destination, id)
				}
				f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, peer)
				requireAdoptionVerification(t, f, reader, 2, 1)
				if undo != "none" {
					store := delivery.NewStore(f.d.App)
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

func TestProjectOnlyExecutorVerifiesHiddenPlacementAndDetectsLoss(t *testing.T) {
	f, reader, executor, destination, member, peer, root := projectOnlyVerificationFixture(t, "project-move")
	mux := http.NewServeMux()
	nodes.New(f.d.App, nil).Mount(mux)
	id := adoptionMove(t, f, mux, "project-move", root, destination)
	store := delivery.NewStore(f.d.App)
	if _, err := store.Place(t.Context(), f.p, destination, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: destination, ExpectedRevision: 0}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{reader, executor} {
		requireAdoptionVerification(t, f, p, 2, 0)
		requireHiddenVerificationEvidence(t, f, p, destination, id)
	}
	// The hidden destination placement supersedes the move's removal. Losing
	// that placement must fail even though the source cannot read its event.
	f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
	requireAdoptionVerification(t, f, reader, 2, 1)
	f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, peer)
	requireAdoptionVerification(t, f, reader, 2, 2)
}

func TestProjectOnlyExecutorRestoresCallerInSameSnapshot(t *testing.T) {
	f, reader, executor, destination, _, _, root := projectOnlyVerificationFixture(t, "project-move")
	mux := http.NewServeMux()
	nodes.New(f.d.App, nil).Mount(mux)
	id := adoptionMove(t, f, mux, "project-move", root, destination)
	capped := f.p
	capped.KeyCreatorID = reader.ID
	for i, caller := range []tenant.Principal{reader, executor, capped} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			err := f.s.snapshot(t.Context(), caller, func(tx pgx.Tx) error {
				settings := `SELECT current_setting('aeon.tenant_id'),current_setting('aeon.visible_projects'),current_setting('aeon.principal_ids'),current_setting('aeon.system'),current_setting('transaction_read_only')`
				var before, after [5]string
				if err := tx.QueryRow(t.Context(), settings).Scan(&before[0], &before[1], &before[2], &before[3], &before[4]); err != nil {
					return err
				}
				out := Verification{JourneyMembers: 2}
				if err := f.s.verifyMembers(t.Context(), tx, caller, f.project, &out); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), settings).Scan(&after[0], &after[1], &after[2], &after[3], &after[4]); err != nil {
					return err
				}
				var exposed int
				if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM nodes WHERE id=$1)+(SELECT count(*) FROM ships_in WHERE project_node_id=$1)+(SELECT count(*) FROM events WHERE id=$2 OR undo_of=$2)`, destination, id).Scan(&exposed); err != nil {
					return err
				}
				if before != after || after[4] != "on" || exposed != 0 || out.AdoptedMembers != 2 || out.MissingArchiveMembers != 0 {
					t.Fatalf("verification changed caller access or lost evidence: before=%q after=%q exposed=%d out=%+v", before, after, exposed, out)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
