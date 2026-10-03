// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"errors"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
)

func TestStoreFix6ReleasedTombstoneMaintenanceUndoUsesWrite(t *testing.T) {
	for _, change := range []string{"none", "revision", "permission"} {
		t.Run(change, func(t *testing.T) {
			f := newStoreFixture(t)
			p, role := f.permissionPerson(t, "releases.write")
			f.requirePermission(t, p, "releases.write", nil)
			f.requirePermission(t, p, "roles.manage", authz.ErrForbidden)
			stale := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			active := f.item(t, "ticket", "TK-2", "open", f.next, "W")
			f.exec(t, `UPDATE ships_in SET expedite=true,due_on='2026-10-10' WHERE item_node_id=$1`, stale)
			f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, stale, f.clock)
			f.releaseHistory(t, f.release)
			originalStale, originalActive := f.placed(t, stale), f.placed(t, active)
			_, err := f.store.Place(t.Context(), p, f.project, []PlacementRequest{{ItemID: active, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1, Expedite: true}})
			if err != nil {
				t.Fatalf("releases.write alone must clean up a released tombstone: %v", err)
			}
			e := f.lastEvent(t, "ships_in.changed")
			if e.ActorPrincipalID != p.ID || f.placed(t, stale).Expedite || !f.placed(t, active).Expedite || f.placed(t, stale).Revision != 2 || f.placed(t, active).Revision != 2 || f.releaseRow(t, f.release).State != "released" {
				t.Fatal("fixture did not perform released tombstone maintenance with the custom role")
			}
			switch change {
			case "revision":
				f.exec(t, `UPDATE ships_in SET revision=revision+1 WHERE item_node_id=$1`, stale)
			case "permission":
				f.exec(t, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='releases.write'`, role)
				f.requirePermission(t, p, "releases.write", authz.ErrForbidden)
			}
			beforeUndoStale, beforeUndoActive := f.placed(t, stale), f.placed(t, active)
			beforeUndoRelease, beforeUndoNext := f.releaseRow(t, f.release), f.releaseRow(t, f.next)
			beforeUndoEvents := f.scalar(t, `SELECT count(*) FROM events`)
			err = f.undo(t, p, e)
			if change != "none" {
				want := error(ErrRevisionChanged)
				if change == "permission" {
					want = authz.ErrForbidden
				}
				if !errors.Is(err, want) {
					t.Fatalf("maintenance undo must recheck %s: %v, want %v", change, err, want)
				}
				if !reflect.DeepEqual(f.placed(t, stale), beforeUndoStale) || !reflect.DeepEqual(f.placed(t, active), beforeUndoActive) || !reflect.DeepEqual(f.releaseRow(t, f.release), beforeUndoRelease) || !reflect.DeepEqual(f.releaseRow(t, f.next), beforeUndoNext) || f.scalar(t, `SELECT count(*) FROM events`) != beforeUndoEvents {
					t.Fatal("refused maintenance undo changed placements, containers or events")
				}
				return
			}
			if err != nil {
				t.Fatalf("releases.write alone must immediately undo released tombstone cleanup: %v", err)
			}
			originalStale.Revision, originalActive.Revision = 3, 3
			if !reflect.DeepEqual(f.placed(t, stale), originalStale) || !reflect.DeepEqual(f.placed(t, active), originalActive) || f.scalar(t, `SELECT count(*) FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL`, stale) != 1 || f.releaseRow(t, f.release).State != "released" || f.releaseRow(t, f.release).Revision != 3 || f.releaseRow(t, f.next).Revision != 3 {
				t.Fatal("undo did not restore only the placements while retaining deletion and release history")
			}
		})
	}
}

func TestStoreFix6LiveHistoryFlagUndoStillNeedsManage(t *testing.T) {
	f := newStoreFixture(t)
	p, role := f.permissionPerson(t, "releases.write", "roles.manage")
	id := f.item(t, "ticket", "TK-1", "open", f.release, "V")
	f.exec(t, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, id)
	f.releaseHistory(t, f.release)
	if _, err := f.store.Place(t.Context(), p, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1}}); err != nil {
		t.Fatal(err)
	}
	e := f.lastEvent(t, "ships_in.changed")
	f.exec(t, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='roles.manage'`, role)
	f.requirePermission(t, p, "roles.manage", authz.ErrForbidden)
	before := f.placed(t, id)
	if err := f.undo(t, p, e); !errors.Is(err, ErrHistoryCorrection) {
		t.Fatalf("a live history flag edit must retain roles.manage authority: %v", err)
	}
	if !reflect.DeepEqual(f.placed(t, id), before) || f.releaseRow(t, f.release).Revision != 2 || f.scalar(t, `SELECT count(*) FROM events`) != 1 {
		t.Fatal("refused live history undo changed state or events")
	}
}
