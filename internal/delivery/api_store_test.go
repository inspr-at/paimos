// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const benefitFields = `{"pill_en":"Captured task","pill_de":"Erfasste Aufgabe","benefit_en":"Keep the exact text.","benefit_de":"Den genauen Text erhalten.","private_comment":"NEVER EXPORT"}`

func decodePreview(t *testing.T, result SnapshotResult) releasehistory.NoteSnapshot {
	t.Helper()
	var s releasehistory.NoteSnapshot
	if e := json.Unmarshal(result.Raw, &s); e != nil {
		t.Fatal(e)
	}
	return s
}
func cutFixture(t *testing.T, f *storeFixture, scheme, version string) Release {
	t.Helper()
	r := f.freeze(t, f.release)
	out, e := f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: r.ID, ExpectedRevision: r.Revision, Action: "cut", VersionScheme: scheme, Version: version})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestCumulativeCaptureCutWindowCarryForwardAndReopenedWait(t *testing.T) {
	f := newStoreFixture(t)
	own := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, own, benefitFields)
	old := f.addRelease(t, f.project, "REL-3", "internal", "released", "F")
	waiting := f.addRelease(t, f.project, "REL-4", "internal", "released", "G")
	gap := f.addRelease(t, f.project, "REL-5", "internal", "released", "H")
	f.exec(t, `UPDATE project_delivery SET adopted_at=$2 WHERE project_node_id=$1`, f.project, f.clock.Add(-time.Hour))
	f.exec(t, `UPDATE project_releases SET released_at=$2 WHERE release_node_id=ANY($1::uuid[])`, []string{old.ID, waiting.ID}, f.clock.Add(-2*time.Hour))
	f.exec(t, `UPDATE project_releases SET released_at=$2 WHERE release_node_id=$1`, gap.ID, f.clock.Add(time.Minute))
	carried := f.item(t, "ticket", "TK-1", "done", old.ID, "V")
	reopened := f.item(t, "ticket", "TK-2", "open", waiting.ID, "V")
	later := f.item(t, "task", "TSK-2", "done", gap.ID, "V")
	f.item(t, "epic", "EP-1", "done", f.release, "W")
	f.item(t, "ticket", "TK-3", "cancelled", f.release, "X")
	for _, id := range []string{carried, reopened, later} {
		f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
	}
	r := cutFixture(t, f, "legacy", "1.2.3-rc.1")
	result, e := f.store.NoteSnapshot(t.Context(), f.person, f.project, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	snapshot := decodePreview(t, result)
	if len(snapshot.Tickets) != 2 || len(result.Waiting) != 1 || result.Waiting[0] != waiting.ID || len(result.CarriedForward) != 1 || result.CarriedForward[0] != old.ID || strings.Contains(string(result.Raw), "NEVER EXPORT") {
		t.Fatalf("selection=%+v snapshot=%+v", result, snapshot)
	}
	var called atomic.Int32
	hook := func(ctx context.Context, tx pgx.Tx, tenantID, releaseID string) error {
		if tenantID != f.tenant || releaseID != r.ID {
			t.Fatal("wrong settlement binding")
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM project_releases WHERE release_node_id=$1`, releaseID).Scan(&state); e != nil {
			return e
		}
		if state != "released" {
			t.Fatal("settlement did not see final write in its transaction")
		}
		called.Add(1)
		return nil
	}
	published, e := f.store.PublishNotes(t.Context(), f.person, PublishRequest{ReleaseEdit: ReleaseEdit{ProjectID: f.project, ReleaseID: r.ID, ExpectedRevision: r.Revision}, ReservationRef: "https://example.invalid/reservation"}, releasehistory.History{}, hook)
	if e != nil {
		t.Fatal(e)
	}
	if published.State != "released" || called.Load() != 1 || f.scalar(t, `SELECT count(*) FROM project_releases WHERE included_in_release_id=$1`, r.ID) != 1 {
		t.Fatal("publication/inclusion did not commit exactly once")
	}
	saved, e := f.store.NoteSnapshot(t.Context(), f.person, f.project, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE nodes SET fields='{"pill_en":"LIVE EDIT"}'::jsonb WHERE id=$1`, own)
	again, e := f.store.NoteSnapshot(t.Context(), f.person, f.project, r.ID)
	if e != nil || string(saved.Raw) != string(again.Raw) {
		t.Fatal("capture was rebuilt from live fields")
	}
	if _, e = releasehistory.ProjectNotesFromSnapshot(saved.Raw, releasehistory.ProjectSnapshotBinding{TenantID: f.tenant, ProjectID: f.project, ReleaseID: r.ID, VersionScheme: r.VersionScheme, Version: r.Version}, "saved"); e != nil {
		t.Fatal(e)
	}
	f.clock = f.clock.Add(2 * time.Minute)
	f.exec(t, `UPDATE nodes SET state='done' WHERE id=$1`, reopened)
	next, e := f.store.NoteSnapshot(t.Context(), f.person, f.project, f.next)
	if e != nil {
		t.Fatal(e)
	}
	if len(decodePreview(t, next).Tickets) != 2 {
		t.Fatalf("next capture missed carried reopened/cut-gap work: %s", next.Raw)
	}
}
func TestInternalOnlyCaptureAndReservationGates(t *testing.T) {
	for _, schemeVersion := range [][2]string{{"legacy", "1.2.3-rc.1"}, {"inspr-calendar-v1", "26.10.03"}, {"inspr-calver-3", "261003120000.0.0"}} {
		t.Run(schemeVersion[0], func(t *testing.T) {
			f := newStoreFixture(t)
			internal := f.addRelease(t, f.project, "REL-3", "internal", "released", "F")
			f.exec(t, `UPDATE project_releases SET released_at=$2 WHERE release_node_id=$1`, internal.ID, f.clock.Add(-time.Minute))
			id := f.item(t, "task", "TSK-1", "done", internal.ID, "V")
			f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
			r := cutFixture(t, f, schemeVersion[0], schemeVersion[1])
			hook := func(context.Context, pgx.Tx, string, string) error { return nil }
			in := PublishRequest{ReleaseEdit: ReleaseEdit{ProjectID: f.project, ReleaseID: r.ID, ExpectedRevision: r.Revision}}
			if _, e := f.store.PublishNotes(t.Context(), f.person, in, releasehistory.History{}, hook); e == nil {
				t.Fatal("missing attestation accepted")
			}
			in.ReservationRef = "reservation:test"
			if _, e := f.store.PublishNotes(t.Context(), f.agent, in, releasehistory.History{}, hook); !errors.Is(e, authz.ErrForbidden) {
				t.Fatalf("agent attestation: %v", e)
			}
			if _, e := f.store.PublishNotes(t.Context(), f.person, in, releasehistory.History{}, hook); e != nil {
				t.Fatal(e)
			}
			if f.scalar(t, `SELECT count(*) FROM outcome_events WHERE kind='released' AND ticket_node_id=$1`, id) != 1 {
				t.Fatal("task capture did not gain one released outcome")
			}
		})
	}
}
func TestProductReservationMatchesExactSchemeVersionSequence(t *testing.T) {
	f := newStoreFixture(t)
	f.exec(t, `UPDATE nodes SET fields='{"project_key":"AEON"}'::jsonb WHERE id=$1`, f.project)
	id := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
	r := cutFixture(t, f, "inspr-calver-3", "261003120000.0.0")
	in := PublishRequest{ReleaseEdit: ReleaseEdit{ProjectID: f.project, ReleaseID: r.ID, ExpectedRevision: r.Revision}}
	h := releasehistory.History{Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: []releasehistory.Release{{Version: r.Version, ReleaseSequence: r.Sequence + 1, State: releasehistory.StateReserved}}}
	hook := func(context.Context, pgx.Tx, string, string) error { return nil }
	if _, e := f.store.PublishNotes(t.Context(), f.agent, in, h, hook); e == nil {
		t.Fatal("wrong sequence accepted")
	}
	h.Releases[0].ReleaseSequence = r.Sequence
	in.ReservationRef = "not-for-product"
	if _, e := f.store.PublishNotes(t.Context(), f.person, in, h, hook); e == nil {
		t.Fatal("product attestation accepted")
	}
	in.ReservationRef = ""
	if _, e := f.store.PublishNotes(t.Context(), f.agent, in, h, hook); e != nil {
		t.Fatal(e)
	}
}
func TestPreviewRetainsSnapshotAcrossCommittedGrowth(t *testing.T) {
	f := newStoreFixture(t)
	id := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "note_preflight") })
	store := NewStore(pool).WithClock(func() time.Time { return f.clock })
	type answer struct {
		r SnapshotResult
		e error
	}
	done := make(chan answer, 1)
	go func() { r, e := store.NoteSnapshot(ctx, f.person, f.project, f.release); done <- answer{r, e} }()
	barrier.Wait(t, ctx)
	f.exec(t, `UPDATE nodes SET fields=jsonb_build_object('benefit_en',repeat('x',2200000)) WHERE id=$1`, id)
	f.exec(t, `UPDATE ships_in SET release_node_id=$2,revision=revision+1 WHERE item_node_id=$1`, id, f.next)
	barrier.Release()
	got := dbtest.Await(t, ctx, done)
	if got.e != nil {
		t.Fatal(got.e)
	}
	snapshot := decodePreview(t, got.r)
	if len(snapshot.Tickets) != 1 || snapshot.Tickets[0].ID != id || !strings.Contains(string(snapshot.Tickets[0].Fields), "Keep the exact text.") {
		t.Fatal("preview crossed committed snapshots")
	}
	if _, e := f.store.NoteSnapshot(t.Context(), f.person, f.project, f.next); e == nil {
		t.Fatal("next preview did not observe oversized growth")
	}
}

type noteReadCounter struct{ pages atomic.Int32 }

func (c *noteReadCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "aeon_release_note_group(n.fields)") {
		c.pages.Add(1)
	}
	return ctx
}
func (*noteReadCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestOversizedNotesRejectBeforeReadingTextAndWriterBounds(t *testing.T) {
	f := newStoreFixture(t)
	id := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=jsonb_build_object('benefit_en',repeat('x',2200000)) WHERE id=$1`, id)
	counter := &noteReadCounter{}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = counter
	pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	_, e = NewStore(pool).NoteSnapshot(t.Context(), f.person, f.project, f.release)
	var conflict *Conflict
	if !errors.As(e, &conflict) || conflict.Code != "notes_too_large" || counter.pages.Load() != 0 {
		t.Fatalf("preflight err=%v pages=%d", e, counter.pages.Load())
	}
	w := &cappedWriter{limit: 5}
	if _, e = w.Write([]byte("12345")); e != nil {
		t.Fatal(e)
	}
	if _, e = w.Write([]byte("6")); !errors.Is(e, ErrNotesTooLarge) || w.Len() != 5 {
		t.Fatal("writer exceeded cap")
	}
}
func TestReadSnapshotOptionsVisibilityAndNoParentDowngrade(t *testing.T) {
	f := newStoreFixture(t)
	ctx := tenant.WithPrincipal(t.Context(), f.person)
	if e := db.ReadSnapshot(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
		var isolation, readonly string
		if e := tx.QueryRow(ctx, `SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only')`).Scan(&isolation, &readonly); e != nil {
			return e
		}
		if isolation != "repeatable read" || readonly != "on" {
			t.Fatal("wrong snapshot options")
		}
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM project_delivery`).Scan(&n); e != nil {
			return e
		}
		if n != 2 {
			t.Fatalf("principal visibility changed: %d", n)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := db.InTransaction(ctx, f.d.App, func(parent context.Context) error {
		return db.ReadSnapshot(parent, f.d.App, f.tenant, func(pgx.Tx) error { t.Fatal("parent snapshot callback ran"); return nil })
	}); e == nil {
		t.Fatal("parent isolation downgraded")
	}
}
func TestReleaseReadsKeysetsRollupAndOverviewNoRecovery(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.item(t, "ticket", "TK-1", "open", f.release, "W")
	f.item(t, "epic", "EP-1", "open", f.release, "X")
	page, e := f.store.ListReleases(t.Context(), f.person, f.project, ReadOptions{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Items) != 1 || page.Items[0].ID != f.release || page.Items[0].Rollup.Units != 2 || page.Items[0].Rollup.Completed != 1 || page.NextCursor == "" {
		t.Fatalf("list=%+v", page)
	}
	next, e := f.store.ListReleases(t.Context(), f.person, f.project, ReadOptions{Limit: 1, Cursor: page.NextCursor})
	if e != nil || len(next.Items) != 1 || next.Items[0].ID != f.next || next.NextCursor != "" {
		t.Fatalf("page2=%+v %v", next, e)
	}
	if _, e := f.store.ListReleases(t.Context(), f.person, f.other, ReadOptions{Cursor: page.NextCursor}); !errors.Is(e, ErrInvalidInput) {
		t.Fatalf("foreign cursor %v", e)
	}
	items, e := f.store.Items(t.Context(), f.person, f.project, f.next, ReadOptions{Through: f.next, Limit: 2})
	if e != nil || len(items.Items) != 2 || items.Count != 3 || items.NextCursor == "" {
		t.Fatalf("through=%+v %v", items, e)
	}
	overview, e := f.store.Overview(t.Context(), f.person, f.project, "")
	if e != nil || len(overview.Active) != 2 || overview.Backlog["done"] != 0 {
		t.Fatalf("overview=%+v %v", overview, e)
	}
}
