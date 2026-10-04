// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
)

func sheetFields(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestSheetProductSchemeCutsAndPublishesRealReservation(t *testing.T) {
	f := newStoreFixture(t)
	f.exec(t, `UPDATE nodes SET fields='{"project_key":"AEON"}'::jsonb WHERE id=$1`, f.project)
	store := f.store.WithProductProject(f.tenant, f.project)
	status, err := store.Status(t.Context(), f.person, f.project)
	if err != nil {
		t.Fatal(err)
	}
	var scheme string
	if err = json.Unmarshal(sheetFields(t, status)["version_scheme"], &scheme); err != nil {
		t.Fatalf("missing authoritative project scheme: %v", err)
	}
	if scheme != releasehistory.SchemeCalVer3 {
		t.Fatalf("product scheme=%q", scheme)
	}
	id := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
	frozen := f.freeze(t, f.release)
	cut, err := store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: f.release, ExpectedRevision: frozen.Revision, Action: "cut", VersionScheme: scheme, Version: "261003120000.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	history := releasehistory.History{Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: []releasehistory.Release{{Version: cut.Version, ReleaseSequence: cut.Sequence, State: releasehistory.StateReserved}}}
	published, err := store.PublishNotes(t.Context(), f.person, PublishRequest{ReleaseEdit: ReleaseEdit{ProjectID: f.project, ReleaseID: cut.ID, ExpectedRevision: cut.Revision}}, history, func(context.Context, pgx.Tx, string, string) error { return nil })
	if err != nil || published.State != "released" {
		t.Fatalf("publish=%+v: %v", published, err)
	}
}

func TestSheetRecoveryMovesCompletedWorkOutOfFrozenSource(t *testing.T) {
	f := newStoreFixture(t)
	f.exec(t, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.next)
	id := f.item(t, "ticket", "TK-1", "done", f.next, "V")
	f.exec(t, `UPDATE project_delivery SET adopted_at=$2 WHERE project_node_id=$1`, f.project, f.clock.Add(-time.Hour))
	f.exec(t, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after,at) VALUES($1,$2,$3,'node.updated','{"state":"open"}','{"state":"done"}',$4)`, f.tenant, f.person.ID, id, f.clock)
	page, err := f.store.Items(t.Context(), f.person, f.project, f.release, ReadOptions{CompletedLater: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ItemID != id {
		t.Fatalf("recovery=%+v", page)
	}
	out, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1, Slot: Slot{Position: "append"}}})
	if err != nil || len(out.Items) != 1 || out.UndoEventID == nil {
		t.Fatalf("Include=%+v: %v", out, err)
	}
	if f.placed(t, id).ReleaseID != f.release || f.releaseRow(t, f.next).State != "frozen" {
		t.Fatal("Include lost source lifecycle or destination identity")
	}
	// Destination Freeze and item CAS remain authoritative after successful removal.
	frozen := f.freeze(t, f.release)
	_, err = f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: f.releaseRow(t, f.next).Revision}})
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("stale item: %v", err)
	}
	_, err = f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 2, ReleaseID: f.next, ExpectedReleaseRevision: f.releaseRow(t, f.next).Revision}})
	if !errors.Is(err, ErrFrozen) || f.releaseRow(t, f.release).Revision != frozen.Revision {
		t.Fatalf("frozen destination: %v", err)
	}
	// A write-only planner needs current write authority for removal too.
	dbtest.BindRole(t, f.d, f.tenant, f.person.ID, "viewer")
	_, err = f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 2}})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revoked planner: %v", err)
	}
}

func TestSheetCutSourceScopeRejectsRemovalAndUndo(t *testing.T) {
	for _, action := range []string{"remove", "rerank", "undo"} {
		t.Run(action, func(t *testing.T) {
			f := newStoreFixture(t)
			source := f.release
			if action == "undo" {
				source = f.next
			}
			id := f.item(t, "ticket", "TK-1", "done", source, "V")
			if action == "rerank" {
				f.item(t, "ticket", "TK-2", "done", f.release, "W")
			}
			var undoID int64
			if action == "undo" {
				out, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1}})
				if err != nil {
					t.Fatal(err)
				}
				undoID = *out.UndoEventID
			}
			cutFixture(t, f, "legacy", "1.0.0")
			before := f.placed(t, id)
			beforeEvents := f.scalar(t, `SELECT count(*) FROM events`)
			var err error
			if action == "undo" {
				event := f.lastEvent(t, "ships_in.changed")
				if event.ID != undoID {
					t.Fatal("wrong Undo event")
				}
				err = f.undo(t, f.person, event)
			} else {
				request := PlacementRequest{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: before.Revision}
				if action == "rerank" {
					request.ReleaseID = f.release
					request.ExpectedReleaseRevision = f.releaseRow(t, f.release).Revision
					request.Slot = Slot{Position: "top"}
				}
				_, err = f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{request})
			}
			if !errors.Is(err, ErrFrozen) {
				t.Fatalf("cut source %s: %v", action, err)
			}
			if !reflect.DeepEqual(before, f.placed(t, id)) || f.scalar(t, `SELECT count(*) FROM events`) != beforeEvents {
				t.Fatal("refused cut-scope change wrote state or events")
			}
		})
	}
}

func TestSheetCapacityCountsEpicsCancelledAndTombstones(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "ticket", "TK-1", "open", f.release, "V")
	f.item(t, "epic", "EP-1", "done", f.release, "W")
	f.item(t, "ticket", "TK-2", "cancelled", f.release, "X")
	deleted := f.item(t, "task", "TSK-1", "done", f.release, "Y")
	f.exec(t, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, deleted)
	view, err := f.store.GetRelease(t.Context(), f.person, f.project, f.release)
	if err != nil {
		t.Fatal(err)
	}
	var occupied int
	if err = json.Unmarshal(sheetFields(t, view)["occupied_rows"], &occupied); err != nil {
		t.Fatalf("missing authoritative membership count: %v", err)
	}
	if occupied != 4 || view.Rollup.Units != 1 {
		t.Fatalf("occupied=%d rollup=%+v", occupied, view.Rollup)
	}
}

func TestSheetLifecycleReceiptsIncludeExactRolloverEvents(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "ticket", "TK-1", "open", f.release, "V")
	frozen := f.freeze(t, f.release)
	assertReceipt := func(r Release, after int64) {
		t.Helper()
		var got, want []int64
		if err := json.Unmarshal(sheetFields(t, r)["event_ids"], &got); err != nil {
			t.Fatalf("missing lifecycle receipt: %v", err)
		}
		f.run(t, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id FROM events WHERE id>$1 ORDER BY id`, after)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				if err = rows.Scan(&id); err != nil {
					return err
				}
				want = append(want, id)
			}
			return rows.Err()
		})
		if len(want) == 0 || !reflect.DeepEqual(got, want) || r.UndoEventID != nil {
			t.Fatalf("receipts=%v committed=%v undo=%v", got, want, r.UndoEventID)
		}
	}
	assertReceipt(frozen, 0)
	last := f.lastEvent(t, "release.state_changed").ID
	cut, err := f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: f.release, ExpectedRevision: frozen.Revision, Action: "cut", VersionScheme: "legacy", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	assertReceipt(cut, last)
	if f.scalar(t, `SELECT count(*) FROM events WHERE type='ships_in.rolled_over'`) != 1 {
		t.Fatal("fixture did not roll over work")
	}
}

func TestSheetPlanSettingsAndPublishReceiptsNeverGrantUndo(t *testing.T) {
	f := newStoreFixture(t)
	assertLast := func(r Release, typ string) {
		t.Helper()
		var ids []int64
		if err := json.Unmarshal(sheetFields(t, r)["event_ids"], &ids); err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != f.lastEvent(t, typ).ID || r.UndoEventID != nil {
			t.Fatalf("receipt=%v undo=%v", ids, r.UndoEventID)
		}
	}
	planned, err := f.store.Plan(t.Context(), f.person, PlanRequest{ProjectID: f.project, Visibility: "internal", Title: "Receipt plan"})
	if err != nil {
		t.Fatal(err)
	}
	assertLast(planned, "release.planned")
	title := "Renamed"
	updated, err := f.store.Update(t.Context(), f.person, UpdateRequest{ProjectID: f.project, ReleaseID: planned.ID, ExpectedRevision: planned.Revision, Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	assertLast(updated, "release.updated")
	// A failed CAS must expose neither event correlation nor Undo authority.
	failed, err := f.store.Update(t.Context(), f.person, UpdateRequest{ProjectID: f.project, ReleaseID: planned.ID, ExpectedRevision: planned.Revision, Title: &title})
	if !errors.Is(err, ErrRevisionChanged) || len(sheetFields(t, failed)["event_ids"]) != 0 || failed.UndoEventID != nil {
		t.Fatalf("failed=%+v: %v", failed, err)
	}
	id := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
	cut := cutFixture(t, f, "legacy", "1.2.0")
	replay, err := f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: cut.ID, ExpectedRevision: cut.Revision, Action: "cut", VersionScheme: cut.VersionScheme, Version: cut.Version})
	if err != nil || len(sheetFields(t, replay)["event_ids"]) != 0 {
		t.Fatalf("no-op=%+v: %v", replay, err)
	}
	published, err := f.store.PublishNotes(t.Context(), f.person, PublishRequest{ReleaseEdit: ReleaseEdit{ProjectID: f.project, ReleaseID: cut.ID, ExpectedRevision: cut.Revision}, ReservationRef: "reservation:test"}, releasehistory.History{}, func(context.Context, pgx.Tx, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	assertLast(published, "release.state_changed")
}
