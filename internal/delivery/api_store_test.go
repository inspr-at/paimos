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
	old := f.addRelease(t, f.project, "REL-3", "internal", "frozen", "F")
	waiting := f.addRelease(t, f.project, "REL-4", "internal", "frozen", "G")
	gap := f.addRelease(t, f.project, "REL-5", "internal", "frozen", "H")
	f.exec(t, `UPDATE project_delivery SET adopted_at=$2 WHERE project_node_id=$1`, f.project, f.clock.Add(-time.Hour))
	f.exec(t, `UPDATE project_releases SET state='released',released_at=$2 WHERE release_node_id=ANY($1::uuid[])`, []string{old.ID, waiting.ID}, f.clock.Add(-2*time.Hour))
	f.exec(t, `UPDATE project_releases SET state='released',released_at=$2 WHERE release_node_id=$1`, gap.ID, f.clock.Add(time.Minute))
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

func TestAdoptedReleasedNotesWithoutCaptureStayUnavailable(t *testing.T) {
	f := newStoreFixture(t)
	id := f.item(t, "task", "TSK-1", "done", f.release, "V")
	f.exec(t, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, benefitFields)
	f.exec(t, `UPDATE project_releases SET state='released',released_at=$2,origin='adopted_released' WHERE release_node_id=$1`, f.release, f.clock)
	result, err := f.store.NoteSnapshot(t.Context(), f.person, f.project, f.release)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Unavailable || len(result.Raw) != 0 {
		t.Fatalf("historical notes rebuilt from current fields: %+v", result)
	}
}
func TestInternalOnlyCaptureAndReservationGates(t *testing.T) {
	for _, schemeVersion := range [][2]string{{"legacy", "1.2.3-rc.1"}, {"inspr-calendar-v1", "26.10.03"}, {"inspr-calver-3", "261003120000.0.0"}} {
		t.Run(schemeVersion[0], func(t *testing.T) {
			f := newStoreFixture(t)
			internal := f.addRelease(t, f.project, "REL-3", "internal", "frozen", "F")
			f.exec(t, `UPDATE project_releases SET state='released',released_at=$2 WHERE release_node_id=$1`, internal.ID, f.clock.Add(-time.Minute))
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

func TestCompletedRecoveryUsesLatestCompletionAfterAdoptionAndSurvivesCut(t *testing.T) {
	f := newStoreFixture(t)
	adopted := f.clock.Add(-time.Hour)
	f.exec(t, `UPDATE project_delivery SET adopted_at=$2 WHERE project_node_id=$1`, f.project, adopted)
	tail := f.item(t, "task", "TSK-1", "done", "", "")
	backlog := f.item(t, "ticket", "TK-1", "done", "", "V")
	early := f.item(t, "task", "TSK-2", "done", f.next, "V")
	old := f.item(t, "ticket", "TK-2", "done", "", "")
	for i, id := range []string{tail, backlog, early, old} {
		at := adopted.Add(time.Duration(i+1) * time.Minute)
		if id == old {
			at = adopted.Add(-time.Minute)
		}
		f.exec(t, `UPDATE nodes SET updated_at=$2 WHERE id=$1`, id, f.clock)
		f.exec(t, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after,at) VALUES($1,$2,$3,'node.updated','{"state":"open"}','{"state":"done"}',$4)`, f.tenant, f.person.ID, id, at)
	}
	page, e := f.store.Items(t.Context(), f.person, f.project, "", ReadOptions{CompletedUnplaced: true, Limit: 1})
	if e != nil || page.Count != 2 || len(page.Items) != 1 || page.Items[0].ItemID != backlog || page.NextCursor == "" {
		t.Fatalf("unplaced %+v %v", page, e)
	}
	next, e := f.store.Items(t.Context(), f.person, f.project, "", ReadOptions{CompletedUnplaced: true, Limit: 1, Cursor: page.NextCursor})
	if e != nil || len(next.Items) != 1 || next.Items[0].ItemID != tail {
		t.Fatalf("next %+v %v", next, e)
	}
	later, e := f.store.Items(t.Context(), f.person, f.project, f.release, ReadOptions{CompletedLater: true})
	if e != nil || later.Count != 1 || later.Items[0].ItemID != early || later.Items[0].ReleaseID != f.next {
		t.Fatalf("later %+v %v", later, e)
	}
	r := cutFixture(t, f, "legacy", "2.0")
	if r.Recovery == nil || r.Recovery.Unplaced != 2 || r.Recovery.Later != 1 || r.Recovery.Incomplete {
		t.Fatalf("omitted counts=%+v", r.Recovery)
	}
	again, e := f.store.Items(t.Context(), f.person, f.project, "", ReadOptions{CompletedUnplaced: true})
	if e != nil || again.Count != 2 {
		t.Fatal("cut lost skipped recovery evidence")
	}
}
func TestPreviewPagesRetainOriginalMembershipAndFields(t *testing.T) {
	f := newStoreFixture(t)
	f.exec(t, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,fields) SELECT $1,$2,'TSK-'||i,'Task',$3,'done',$4::jsonb FROM generate_series(1,501) i`, f.tenant, f.kinds["task"], f.project, benefitFields)
	f.exec(t, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by) SELECT $1,$2,id,$3,'V'||lpad(split_part(key,'-',2),4,'0')||'V','person',$4 FROM nodes WHERE tenant_id=$1 AND project_id=$2 AND key LIKE 'TSK-%'`, f.tenant, f.project, f.release, f.person.ID)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "aeon_release_note_group(n.fields)") })
	store := NewStore(pool).WithClock(func() time.Time { return f.clock })
	type answer struct {
		r SnapshotResult
		e error
	}
	done := make(chan answer, 1)
	go func() { r, e := store.NoteSnapshot(ctx, f.person, f.project, f.release); done <- answer{r, e} }()
	barrier.Wait(t, ctx)
	f.exec(t, `UPDATE nodes SET fields=jsonb_build_object('benefit_en',repeat('x',2200000)) WHERE tenant_id=$1 AND project_id=$2 AND key='TSK-501'`, f.tenant, f.project)
	f.exec(t, `UPDATE ships_in SET release_node_id=$2,revision=revision+1 WHERE item_node_id=(SELECT id FROM nodes WHERE tenant_id=$1 AND key='TSK-501')`, f.tenant, f.next)
	barrier.Release()
	got := dbtest.Await(t, ctx, done)
	if got.e != nil {
		t.Fatal(got.e)
	}
	s := decodePreview(t, got.r)
	if len(s.Tickets) != 501 || s.Tickets[500].Key != "TSK-501" || !strings.Contains(string(s.Tickets[500].Fields), "Keep the exact text.") {
		t.Fatal("page crossed the snapshot")
	}
	seen := map[string]bool{}
	for _, ticket := range s.Tickets {
		if seen[ticket.ID] {
			t.Fatal("duplicate page member")
		}
		seen[ticket.ID] = true
	}
}
func TestSnapshotCancellationReturnsNoPartialBytes(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "task", "TSK-1", "done", f.release, "V")
	pool, barrier, parent := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "note_preflight") })
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type answer struct {
		r SnapshotResult
		e error
	}
	done := make(chan answer, 1)
	go func() { r, e := NewStore(pool).NoteSnapshot(ctx, f.person, f.project, f.release); done <- answer{r, e} }()
	barrier.Wait(t, parent)
	cancel()
	barrier.Release()
	got := dbtest.Await(t, parent, done)
	if !errors.Is(got.e, context.Canceled) || len(got.r.Raw) != 0 {
		t.Fatalf("partial cancelled export %d %v", len(got.r.Raw), got.e)
	}
}

type eventQueryCounter struct{ queries atomic.Int32 }

func (c *eventQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "FROM events") {
		c.queries.Add(1)
	}
	return ctx
}
func (*eventQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestOverviewDoesNotLookUpCompletionEvents(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "task", "TSK-1", "done", "", "")
	counter := &eventQueryCounter{}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = counter
	pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if _, err := NewStore(pool).Items(t.Context(), f.person, f.project, "", ReadOptions{CompletedUnplaced: true}); err != nil || counter.queries.Load() == 0 {
		t.Fatalf("trace negative control: %v %d", err, counter.queries.Load())
	}
	counter.queries.Store(0)
	out, e := NewStore(pool).Overview(t.Context(), f.person, f.project, "")
	if e != nil || len(out.Active) != 2 || counter.queries.Load() != 0 {
		t.Fatalf("overview=%+v err=%v event queries=%d", out, e, counter.queries.Load())
	}
	if _, present := out.Backlog["done"]; present {
		t.Fatal("overview exposed a done backlog count")
	}
}
func TestAdoptionInventoryIsPermissionScopedAndIncludesDeletedOnlyForOperators(t *testing.T) {
	f := newStoreFixture(t)
	f.exec(t, `INSERT INTO delivery_adoption_jobs(tenant_id,project_node_id,instance_id,rollout_artifact_ref,executing_principal_id,authorizing_principal_id,rollout_authorization_ref,state,reason_code,reason_message) VALUES($1,$2,'test','release:E',$3,$3,'authorization:test','refused','deleted_project','Repair this project')`, f.tenant, f.legacy, f.person.ID)
	f.exec(t, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.legacy)
	owner, e := f.store.Adoptions(t.Context(), f.person, ReadOptions{Limit: 1})
	if e != nil || owner.Status != "needs_attention" || owner.Counts["refused"] != 1 || owner.Counts["adopted"] != 2 || owner.NextCursor == "" {
		t.Fatalf("inventory=%+v %v", owner, e)
	}
	seen := map[string]bool{}
	for {
		for _, v := range owner.Items {
			if seen[v.ProjectID] {
				t.Fatal("duplicate inventory row")
			}
			seen[v.ProjectID] = true
		}
		if owner.NextCursor == "" {
			break
		}
		owner, e = f.store.Adoptions(t.Context(), f.person, ReadOptions{Limit: 1, Cursor: owner.NextCursor})
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 3 || !seen[f.legacy] {
		t.Fatal("operator lost refused deleted project")
	}
	status, e := f.store.Status(t.Context(), f.person, f.legacy)
	if e != nil || status.Adoption == nil || status.Adoption.State != "refused" {
		t.Fatalf("operator deleted status: %+v %v", status, e)
	}
	reporter := &inventoryReporter{}
	if _, e := f.store.AdoptionReport(t.Context(), f.person, f.legacy, "", 1, reporter); e != nil || reporter.calls != 1 {
		t.Fatalf("operator deleted report: %v", e)
	}
	dbtest.BindRole(t, f.d, f.tenant, f.person.ID, "member")
	if _, e := f.store.AdoptionReport(t.Context(), f.person, f.legacy, "", 1, reporter); !errors.Is(e, ErrNotFound) || reporter.calls != 1 {
		t.Fatalf("deleted report leaked to member: %v", e)
	}
	member, e := f.store.Adoptions(t.Context(), f.person, ReadOptions{})
	if e != nil || len(member.Items) != 2 || member.Counts["refused"] != 0 {
		t.Fatalf("member inventory=%+v %v", member, e)
	}
	if _, e := f.store.Adoptions(t.Context(), f.agent, ReadOptions{}); !errors.Is(e, authz.ErrForbidden) {
		t.Fatalf("agent without read scope saw inventory: %v", e)
	}
}
func TestCutLostResponseReplaysOriginalRevisionWithoutEvents(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "task", "TSK-1", "done", f.release, "V")
	frozen := f.freeze(t, f.release)
	in := TransitionRequest{ProjectID: f.project, ReleaseID: f.release, ExpectedRevision: frozen.Revision, Action: "cut", VersionScheme: "legacy", Version: "26.10.03"}
	cut, e := f.store.Transition(t.Context(), f.person, in)
	if e != nil {
		t.Fatal(e)
	}
	before := f.scalar(t, `SELECT count(*) FROM events`)
	replayed, e := f.store.Transition(t.Context(), f.person, in)
	if e != nil || replayed.Revision != cut.Revision || f.scalar(t, `SELECT count(*) FROM events`) != before {
		t.Fatalf("lost response replay=%+v %v", replayed, e)
	}
	in.VersionScheme = "inspr-calendar-v1"
	if _, e := f.store.Transition(t.Context(), f.person, in); !errors.Is(e, ErrTransition) {
		t.Fatalf("replay scheme refusal: %v", e)
	}
}

type inventoryReporter struct{ calls int }

func (r *inventoryReporter) ReadReport(context.Context, tenant.Principal, string, string, int) (AdoptionReport, error) {
	r.calls++
	return AdoptionReport{Items: []json.RawMessage{json.RawMessage(`{"reason":"deleted project"}`)}, Revision: 1}, nil
}
func (*inventoryReporter) RequestTx(context.Context, pgx.Tx, tenant.Principal, string, string, int64) (AdoptionJob, error) {
	return AdoptionJob{}, errors.New("read-only fixture")
}
