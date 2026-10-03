// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestPlacementReceiptNamesExactCommittedBatchAndRevisions(t *testing.T) {
	f := newStoreFixture(t)
	a := f.item(t, "ticket", "TK-1", "open", f.release, "V")
	b := f.item(t, "ticket", "TK-2", "open", f.release, "W")
	result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{
		{ItemID: a, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1},
		{ItemID: b, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UndoEventID == nil {
		t.Fatal("missing exact receipt")
	}
	e := f.lastEvent(t, "ships_in.changed")
	if *result.UndoEventID != e.ID || result.ReleaseRevisions[f.release] != f.releaseRow(t, f.release).Revision || result.ReleaseRevisions[f.next] != result.ReleaseRevision {
		t.Fatalf("receipt/revisions %+v; event %d", result, e.ID)
	}
	var after placementSnapshot
	if err := json.Unmarshal(e.After, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Members) != 2 {
		t.Fatalf("batch not atomic: %+v", after)
	}
	// A newer unrelated event must not be selected or undone by this receipt.
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, f.person, events.Change{NodeID: &f.project, Type: "test.unrelated", After: map[string]any{"project_id": f.project}})
		return err
	})
	if err := f.undo(t, f.person, e); err != nil {
		t.Fatal(err)
	}
	if f.placed(t, a).ReleaseID != f.release || f.placed(t, b).ReleaseID != f.release {
		t.Fatal("receipt did not restore exact batch")
	}
}
func TestPlacementRefusalReturnsNoReceiptAndDoesNotCommit(t *testing.T) {
	f := newStoreFixture(t)
	a := f.item(t, "ticket", "TK-1", "open", f.release, "V")
	before := f.scalar(t, `SELECT count(*) FROM events`)
	result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: a, ExpectedProjectID: f.project, ExpectedRevision: 999, ReleaseID: f.next, ExpectedReleaseRevision: 1}})
	if !errors.Is(err, ErrRevisionChanged) || result.UndoEventID != nil || len(result.Items) != 0 {
		t.Fatalf("wrong refusal %+v %v", result, err)
	}
	if f.scalar(t, `SELECT count(*) FROM events`) != before || f.placed(t, a).ReleaseID != f.release {
		t.Fatal("failed write committed")
	}
}
func TestRankReceiptNamesItsOwnEventAndUndoCAS(t *testing.T) {
	f := newStoreFixture(t)
	r := f.addRelease(t, f.project, "REL-3", "internal", "planned", "Z")
	result, err := f.store.Rerank(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: r.ID, ExpectedRevision: r.Revision, Slot: Slot{BeforeID: f.release}})
	if err != nil {
		t.Fatal(err)
	}
	e := f.lastEvent(t, "release.reranked")
	if result.UndoEventID == nil || *result.UndoEventID != e.ID || result.Revision != f.releaseRow(t, r.ID).Revision {
		t.Fatalf("receipt %+v event %d", result, e.ID)
	}
	_, err = f.store.Rerank(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: r.ID, ExpectedRevision: result.Revision, Slot: Slot{AfterID: f.next}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.undo(t, f.person, e); !errors.Is(err, events.ErrConflict) {
		t.Fatalf("stale receipt must fail CAS: %v", err)
	}
}
