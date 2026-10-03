// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func adoptedReaderFixture(t *testing.T) (*fixture, tenant.Principal, string) {
	t.Helper()
	f := newFixture(t)
	release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	member := f.member(t, f.project, release, "TK-1", 1)
	reader := adoptionProjectReader(t, f)
	j := f.prepare(t, f.project)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	return f, reader, member
}

func TestVerificationKeepsOriginalReaderAndJobAuthorityBoundaries(t *testing.T) {
	for _, which := range []string{"original-reader", "missing-authority", "different-executor", "different-authorizer", "different-reference", "inactive-executor", "revoked-executor"} {
		t.Run(which, func(t *testing.T) {
			f, reader, _ := adoptedReaderFixture(t)
			want := ErrPrerequisite
			switch which {
			case "original-reader":
				f.sql(t, `DELETE FROM role_bindings WHERE principal_id=$1`, reader.ID)
				want = authz.ErrForbidden
			case "missing-authority":
				f.s.cfg.Authorities = nil
			case "different-executor":
				f.s.cfg.Authorities[0].Executor = reader
			case "different-authorizer":
				f.s.cfg.Authorities[0].Authorizer = reader
			case "different-reference":
				f.s.cfg.Authorities[0].Reference = "unbound-authority"
			case "inactive-executor":
				f.secondOwner(t)
				f.sql(t, `UPDATE principals SET status='deactivated' WHERE id=$1`, f.p.ID)
				want = authz.ErrForbidden
			case "revoked-executor":
				f.secondOwner(t)
				f.sql(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.p.ID)
				want = authz.ErrForbidden
			}
			v, err := f.s.Verify(t.Context(), reader, f.project)
			if !errors.Is(err, want) || v.OK {
				t.Fatalf("%s: %+v, %v; want %v", which, v, err, want)
			}
		})
	}
}

func TestVerificationRestoresKeyCreatorVisibilityInSameSnapshot(t *testing.T) {
	f, reader, _ := adoptedReaderFixture(t)
	hidden := f.node(t, "project", "DST-1", "", "open")
	// The workspace executor used as a request principal is capped by its
	// project-only key creator. Restoring only its ID would open the workspace.
	capped := f.p
	capped.KeyCreatorID = reader.ID
	var before, after string
	err := f.s.snapshot(t.Context(), capped, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.visible_projects')`).Scan(&before); err != nil {
			return err
		}
		out := Verification{JourneyMembers: 1}
		if err := f.s.verifyMembers(t.Context(), tx, capped, f.project, &out); err != nil {
			return err
		}
		var exposed int
		if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.visible_projects'),(SELECT count(*) FROM nodes WHERE id=$1)`, hidden).Scan(&after, &exposed); err != nil {
			return err
		}
		if out.AdoptedMembers != 1 || out.MissingArchiveMembers != 0 || exposed != 0 || before != after {
			t.Fatalf("verification widened caller visibility: %+v, %d, %q -> %q", out, exposed, before, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerificationCountsRetainedPlacementInHiddenDestination(t *testing.T) {
	f, reader, member := adoptedReaderFixture(t)
	destination := f.node(t, "project", "DST-1", "", "open")
	j := f.prepare(t, destination)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	store := delivery.NewStore(f.d.App)
	if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	nodes.New(f.d.App, nil).Mount(mux)
	adoptionMove(t, f, mux, "project-move", member, destination)
	if _, err := store.Place(t.Context(), f.p, destination, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: destination, ExpectedRevision: 0}}); err != nil {
		t.Fatal(err)
	}
	requireAdoptionVerification(t, f, reader, 1, 0)
	f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
	// The placement event in the hidden destination supersedes the old move;
	// an unexplained deletion there must still be detected from the source.
	requireAdoptionVerification(t, f, reader, 1, 1)
}

func TestVerificationDoesNotAttributeDepartedDescendantToAncestorUndo(t *testing.T) {
	f, reader, member := adoptedReaderFixture(t)
	root := f.node(t, "epic", "EP-1", f.project, "open")
	f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, root)
	destination := f.node(t, "project", "DST-1", "", "open")
	j := f.prepare(t, destination)
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
	ancestorMove := adoptionMove(t, f, mux, "subtree", root, destination)
	// The member leaves the ancestor before its Undo. Its new placement can
	// no longer be removed by returning that ancestor to the original project.
	adoptionMove(t, f, mux, "move", member, destination)
	if _, err := store.Place(t.Context(), f.p, destination, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: destination, ExpectedRevision: 0}}); err != nil {
		t.Fatal(err)
	}
	f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
	adoptionMutationRequest(t, mux, f.p, fmt.Sprintf("/api/events/%d/undo", ancestorMove), "", http.StatusCreated)
	if f.count(t, `SELECT count(*) FROM nodes WHERE id=$1 AND project_id=$2`, member, destination) != 1 || f.count(t, `SELECT count(*) FROM events WHERE undo_of=$1 AND coalesce(jsonb_array_length(before->'ships_in_before_move'),0)=0`, ancestorMove) != 1 {
		t.Fatal("ancestor Undo unexpectedly moved or removed the departed descendant")
	}
	requireAdoptionVerification(t, f, f.p, 1, 1)
	requireAdoptionVerification(t, f, reader, 1, 1)
}
