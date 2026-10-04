// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"errors"
	"testing"
)

func TestExplicitPositionsValidateBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		position, before, after string
		valid                   bool
	}{
		{"", "", "", true}, {"top", "", "", true}, {"append", "", "", true},
		{"end", "", "", false}, {"top", "anchor", "", false}, {"append", "", "anchor", false},
	} {
		if got := ValidPosition(tc.position, tc.before, tc.after); got != tc.valid {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}

func TestPlacementSingleAnchorKeepsHiddenPhysicalRanksAndExactReceipt(t *testing.T) {
	f := newStoreFixture(t)
	a := f.item(t, "ticket", "TK-1", "open", f.next, "V")
	tombstone := f.item(t, "ticket", "TK-2", "open", f.next, "W")
	b := f.item(t, "ticket", "TK-3", "done", f.next, "X")
	moved := f.item(t, "ticket", "TK-4", "open", f.release, "V")
	f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, tombstone, f.clock)
	result, err := f.store.PlaceWithRevision(t.Context(), f.agent, f.project, []PlacementRequest{{ItemID: moved, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1, Slot: Slot{AfterID: a}, PreserveExpedite: true, PreserveDueOn: true}})
	if err != nil {
		t.Fatal(err)
	}
	p := result.Items[0]
	if p.Rank <= "V" || p.Rank >= "W" || p.ReleaseID != f.next || result.UndoEventID == nil || *result.UndoEventID != f.lastEvent(t, "ships_in.changed").ID {
		t.Fatalf("not a single final-gap write: %+v", result)
	}
	if f.placed(t, tombstone).Rank != "W" || f.placed(t, b).Rank != "X" {
		t.Fatal("occupied hidden keys changed")
	}
	// No intermediate append/rerank: an agent can directly insert into a later
	// release, even when moving to this gap from an intermediate append would promote.
	if p.Revision != 2 {
		t.Fatalf("more than one write: %+v", p)
	}
	if err = f.undo(t, f.agent, f.lastEvent(t, "ships_in.changed")); !errors.Is(err, ErrPromotion) {
		t.Fatalf("agent undo must reauthorize promotion: %v", err)
	}
	if err = f.undo(t, f.person, f.lastEvent(t, "ships_in.changed")); err != nil {
		t.Fatal(err)
	}
	if got := f.placed(t, moved); got.ReleaseID != f.release || got.Rank != "V" {
		t.Fatalf("wrong exact undo: %+v", got)
	}
}
func TestDirectAnchorRefusesDeletedMovedConvertedAndTail(t *testing.T) {
	for _, state := range []string{"deleted", "moved", "converted", "tail"} {
		t.Run(state, func(t *testing.T) {
			f := newStoreFixture(t)
			a := f.item(t, "ticket", "TK-1", "open", f.next, "V")
			moved := f.item(t, "ticket", "TK-2", "open", f.release, "V")
			destination := f.next
			switch state {
			case "deleted":
				f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, a, f.clock)
			case "moved":
				f.exec(t, `UPDATE ships_in SET release_node_id=$2,rank='W' WHERE item_node_id=$1`, a, f.release)
			case "converted":
				destination = ""
				f.exec(t, `UPDATE ships_in SET release_node_id=NULL WHERE item_node_id=$1`, a)
				f.exec(t, `UPDATE nodes SET kind_id=$2 WHERE id=$1`, a, f.kinds["release"])
			case "tail":
				destination = ""
				a = f.item(t, "ticket", "TK-3", "open", "", "")
			}
			before := f.scalar(t, `SELECT count(*) FROM events`)
			result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: moved, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: destination, ExpectedReleaseRevision: 1, Slot: Slot{AfterID: a}}})
			if !errors.Is(err, ErrNotFound) || result.UndoEventID != nil {
				t.Fatalf("wrong refusal: %+v %v", result, err)
			}
			if f.scalar(t, `SELECT count(*) FROM events`) != before || f.placed(t, moved).Revision != 1 {
				t.Fatal("refusal committed")
			}
		})
	}
}
func TestExplicitTopPrecedesTombstoneAndAppendPrecedesTail(t *testing.T) {
	f := newStoreFixture(t)
	tombstone := f.item(t, "ticket", "TK-1", "open", "", "V")
	f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, tombstone, f.clock)
	tail := f.item(t, "ticket", "TK-2", "open", "", "")
	moved := f.item(t, "ticket", "TK-3", "open", f.release, "V")
	result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: moved, ExpectedProjectID: f.project, ExpectedRevision: 1, Slot: Slot{Position: "top"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Rank >= "V" || result.Items[0].Rank == "" {
		t.Fatalf("not physical top: %+v", result)
	}
	result, err = f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: moved, ExpectedProjectID: f.project, ExpectedRevision: 2, Slot: Slot{Position: "append"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Rank <= "V" || f.placed(t, tail).Revision != 0 {
		t.Fatalf("append changed tail or skipped occupied key: %+v", result)
	}
}
func TestReleaseExtremaUseUpcomingAndKeepTerminalKeys(t *testing.T) {
	f := newStoreFixture(t)
	// Internal terminal keys bracket Upcoming; top/end mean that section only.
	low := f.addRelease(t, f.project, "REL-3", "internal", "planned", "A")
	high := f.addRelease(t, f.project, "REL-4", "internal", "planned", "X")
	f.exec(t, `UPDATE project_releases SET state='abandoned',abandoned_at=$2 WHERE release_node_id=ANY($1::uuid[])`, []string{low.ID, high.ID}, f.clock)
	subject := f.addRelease(t, f.project, "REL-5", "internal", "planned", "Z")
	result, err := f.store.Rerank(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: subject.ID, ExpectedRevision: 1, Slot: Slot{Position: "top"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rank <= low.Rank || result.Rank >= "B" || result.UndoEventID == nil {
		t.Fatalf("not Upcoming top: %+v", result)
	}
	result, err = f.store.Rerank(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: subject.ID, ExpectedRevision: 2, Slot: Slot{Position: "append"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rank <= "D" || result.Rank >= high.Rank {
		t.Fatalf("not Upcoming end: %+v", result)
	}
}

func TestBeforeAnchorAndLegacyPairAcrossTombstone(t *testing.T) {
	f := newStoreFixture(t)
	a := f.item(t, "ticket", "TK-1", "open", f.next, "V")
	hidden := f.item(t, "ticket", "TK-2", "open", f.next, "W")
	b := f.item(t, "ticket", "TK-3", "done", f.next, "X")
	subject := f.item(t, "ticket", "TK-4", "open", f.release, "V")
	f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, hidden, f.clock)
	request := PlacementRequest{ItemID: subject, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1, Slot: Slot{AfterID: a, BeforeID: b}}
	before := f.scalar(t, `SELECT count(*) FROM events`)
	result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{request})
	var conflict *Conflict
	if !errors.As(err, &conflict) || conflict.Code != "neighbours_changed" || result.UndoEventID != nil || f.scalar(t, `SELECT count(*) FROM events`) != before {
		t.Fatalf("legacy pair must refuse physical non-adjacency: %+v %v", result, err)
	}
	request.Slot = Slot{BeforeID: b}
	result, err = f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{request})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Items[0].Rank; got <= "W" || got >= "X" {
		t.Fatalf("before must resolve its physical predecessor, got %s", got)
	}
}
func TestTopInEmptyRankedBacklogDoesNotRankTheTail(t *testing.T) {
	f := newStoreFixture(t)
	tail := f.item(t, "ticket", "TK-1", "open", "", "")
	subject := f.item(t, "ticket", "TK-2", "open", f.release, "V")
	result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: subject, ExpectedProjectID: f.project, ExpectedRevision: 1, Slot: Slot{Position: "top"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Rank == "" || f.placed(t, tail).Revision != 0 {
		t.Fatalf("empty ranked portion must accept top without an invented anchor: %+v", result)
	}
}
