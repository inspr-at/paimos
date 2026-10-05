// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestPlacementReceiptHundredItemsIncludesExpediteDisplacement(t *testing.T) {
	f := newStoreFixture(t)
	stale := f.item(t, "ticket", "OLD-1", "done", f.release, "V")
	f.exec(t, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, stale)
	requests := make([]PlacementRequest, 0, 100)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		for i := 0; i < 100; i++ {
			id := f.node(t, ctx, tx, "ticket", fmt.Sprintf("BATCH-%d", i+1), f.project)
			requests = append(requests, PlacementRequest{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 0, ReleaseID: f.release, ExpectedReleaseRevision: 1, Expedite: i == 0})
		}
		return nil
	})
	result, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, requests)
	if err != nil {
		t.Fatal(err)
	}
	if result.UndoEventID == nil || len(result.Items) != 101 {
		t.Fatalf("incomplete receipt %+v", result)
	}
	e := f.lastEvent(t, "ships_in.changed")
	if e.ID != *result.UndoEventID {
		t.Fatal("wrong receipt")
	}
	if err = f.undo(t, f.person, e); err != nil {
		t.Fatal(err)
	}
	if !f.placed(t, stale).Expedite {
		t.Fatal("displaced expedite not restored")
	}
	for _, r := range requests {
		if f.placed(t, r.ItemID).Revision != 0 {
			t.Fatal("batch member not restored to tail")
		}
	}
}

func TestReleaseAgentSummariesBoundedAndPermissionAware(t *testing.T) {
	f := newStoreFixture(t)
	a := f.item(t, "ticket", "AGT-1", "open", f.release, "V")
	b := f.item(t, "ticket", "AGT-2", "open", f.release, "W")
	var run string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		order := f.node(t, ctx, tx, "work_order", "WOR-1", f.project)
		if _, err := tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.tenant, order, f.person.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status) VALUES($1,$2,$3,'waiting') RETURNING id::text`, f.tenant, order, f.agent.ID).Scan(&run); err != nil {
			return err
		}
		for i, id := range []string{a, b} {
			if _, err := tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,run_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase) VALUES($1,$2,$3,$4,$5,'codex','test','unmanaged','worker','ship',decode($6,'hex'),decode('02','hex'),'working')`, f.tenant, f.project, f.agent.ID, id, run, fmt.Sprintf("%02x", i+1)); err != nil {
				return err
			}
		}
		return nil
	})
	page, err := f.store.ListReleases(t.Context(), f.person, f.project, ReadOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Items {
		want := 0
		if r.ID == f.release {
			want = 1
		}
		if r.BuildSummary["agents"] != want || r.BuildSummary["waiting"] != want || r.BuildSummary["agents_incomplete"] != false {
			t.Fatalf("summary %+v", r.BuildSummary)
		}
	}
	reader := f.agent
	reader.Scopes = append(reader.Scopes, "releases.read")
	page, err = f.store.ListReleases(t.Context(), reader, f.project, ReadOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Items {
		if _, ok := r.BuildSummary["agents"]; ok {
			t.Fatal("agents leaked without harness.read")
		}
	}
	f.exec(t, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase) SELECT $1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(lpad(to_hex(i),8,'0'),'hex'),decode('03','hex'),'working' FROM generate_series(1,5000) i`, f.tenant, f.project, f.agent.ID, a)
	page, err = f.store.ListReleases(t.Context(), f.person, f.project, planningOpt(t, "hide_closed=false"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Items {
		if r.BuildSummary["agents_incomplete"] != true {
			t.Fatal("truncated aggregate claimed complete")
		}
	}
}
