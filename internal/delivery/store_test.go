// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type storeFixture struct {
	*readFixture
	store         *Store
	person, agent tenant.Principal
	clock         time.Time
}

func newStoreFixture(t *testing.T) *storeFixture {
	f := &storeFixture{readFixture: newReadFixture(t), clock: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	f.person = tenant.Principal{ID: f.actor, TenantID: f.tenant, Kind: tenant.Person}
	dbtest.BindRole(t, f.d, f.tenant, f.actor, "owner")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		f.agent = tenant.Principal{TenantID: f.tenant, Kind: tenant.Agent, Scopes: []string{"nodes.read", "releases.write", "releases.deploy"}, KeyCreatorID: f.actor}
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Release agent') RETURNING id::text`, f.tenant).Scan(&f.agent.ID)
	})
	dbtest.BindRole(t, f.d, f.tenant, f.agent.ID, "owner")
	f.store = NewStore(f.d.App).WithClock(func() time.Time { return f.clock })
	return f
}

func (f *storeFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	f.run(t, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q, args...); return err })
}
func (f *storeFixture) scalar(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	f.run(t, func(ctx context.Context, tx pgx.Tx) error { return tx.QueryRow(ctx, q, args...).Scan(&n) })
	return n
}
func (f *storeFixture) releaseRow(t *testing.T, id string) Release {
	t.Helper()
	var r Release
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		r, err = scanRelease(tx.QueryRow(ctx, `SELECT `+releaseColumns+` FROM project_releases WHERE release_node_id=$1`, id))
		return err
	})
	return r
}
func (f *storeFixture) placed(t *testing.T, id string) Placement {
	t.Helper()
	var p Placement
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT item_node_id::text,project_node_id::text,coalesce(release_node_id::text,''),rank,revision,expedite,due_on::text FROM ships_in WHERE item_node_id=$1`, id).Scan(&p.ItemID, &p.ProjectID, &p.ReleaseID, &p.Rank, &p.Revision, &p.Expedite, &p.DueOn)
		if errors.Is(err, pgx.ErrNoRows) {
			p = Placement{ItemID: id, ProjectID: f.project}
			return nil
		}
		return err
	})
	return p
}
func (f *storeFixture) item(t *testing.T, kind, key, state, release, rank string) string {
	t.Helper()
	var id string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		id = f.node(t, ctx, tx, kind, key, f.project)
		if _, err := tx.Exec(ctx, `UPDATE nodes SET state=$2,updated_at=clock_timestamp() WHERE id=$1`, id, state); err != nil {
			return err
		}
		if rank != "" {
			f.place(t, ctx, tx, id, release, rank)
		}
		return nil
	})
	return id
}
func (f *storeFixture) addRelease(t *testing.T, project, key, visibility, state, rank string) Release {
	t.Helper()
	var id string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		id = f.node(t, ctx, tx, "release", key, project)
		_, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,state,rank) VALUES($1,$2,$3,$4,$5,$6)`, f.tenant, project, id, visibility, state, rank)
		return err
	})
	return f.releaseRow(t, id)
}
func (f *storeFixture) freeze(t *testing.T, id string) Release {
	t.Helper()
	r := f.releaseRow(t, id)
	out, err := f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: r.ProjectID, ReleaseID: id, ExpectedRevision: r.Revision, Action: "freeze"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *storeFixture) lastEvent(t *testing.T, typ string) events.Event {
	t.Helper()
	var e events.Event
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at FROM events WHERE type=$1 ORDER BY id DESC LIMIT 1`, typ).Scan(&e.ID, &e.ActorPrincipalID, &e.NodeID, &e.Type, &e.Before, &e.After, &e.At)
	})
	return e
}
func (f *storeFixture) undo(t *testing.T, p tenant.Principal, e events.Event) error {
	t.Helper()
	ctx := tenant.WithPrincipal(t.Context(), p)
	return db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error { _, err := f.store.UndoHandlers()[e.Type](ctx, tx, p, e); return err })
}

func TestStoreTailCASAndHTTPUndo(t *testing.T) {
	f := newStoreFixture(t)
	id := f.item(t, "ticket", "TK-1", "open", "", "")
	request := PlacementRequest{ItemID: id, ExpectedProjectID: f.project, ReleaseID: f.release, ExpectedReleaseRevision: 1}
	out, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{request})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Revision != 1 || out[0].Rank != "V" || out[0].ReleaseID != f.release {
		t.Fatalf("insert=%+v", out)
	}
	if f.releaseRow(t, f.release).Revision != 2 {
		t.Fatal("placement did not fence release revision")
	}
	_, err = f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{request})
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("stale tail: %v", err)
	}
	e := f.lastEvent(t, "ships_in.changed")
	mux := http.NewServeMux()
	events.New(f.d.App, events.WithUndoHandlers(f.store.UndoHandlers())).Mount(mux)
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/events/%d/undo", e.ID), nil).WithContext(tenant.WithPrincipal(t.Context(), f.person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call()
	if w.Code != 201 {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if got := f.placed(t, id); got.Revision != 0 || got.Rank != "" || got.ReleaseID != "" {
		t.Fatalf("tail not restored: %+v", got)
	}
	if got := call(); got.Code != 409 {
		t.Fatalf("double undo: %d %s", got.Code, got.Body.String())
	}
	if n := f.scalar(t, `SELECT count(*) FROM events WHERE undo_of=$1`, e.ID); n != 1 {
		t.Fatalf("compensations=%d", n)
	}
}

func TestStoreBatchAndAgentUndoCannotPromote(t *testing.T) {
	f := newStoreFixture(t)
	a := f.item(t, "ticket", "TK-1", "open", f.release, "V")
	tail := f.item(t, "task", "TSK-1", "open", "", "")
	move := PlacementRequest{ItemID: a, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}
	promote := PlacementRequest{ItemID: tail, ExpectedProjectID: f.project, ReleaseID: f.next, ExpectedReleaseRevision: 1}
	_, err := f.store.Place(t.Context(), f.agent, f.project, []PlacementRequest{move, promote})
	if !errors.Is(err, ErrPromotion) {
		t.Fatalf("batch promotion: %v", err)
	}
	if f.placed(t, a).ReleaseID != f.release || f.placed(t, a).Revision != 1 || f.placed(t, tail).Revision != 0 || f.scalar(t, `SELECT count(*) FROM events`) != 0 {
		t.Fatal("batch failure left partial effects")
	}
	if _, err = f.store.Place(t.Context(), f.agent, f.project, []PlacementRequest{move}); err != nil {
		t.Fatal(err)
	}
	e := f.lastEvent(t, "ships_in.changed")
	if err = f.undo(t, f.agent, e); !errors.Is(err, ErrPromotion) || !errors.Is(err, events.ErrConflict) {
		t.Fatalf("agent undo promotion: %v", err)
	}
	if f.placed(t, a).ReleaseID != f.next {
		t.Fatal("refused undo moved item")
	}
	if err = f.undo(t, f.person, e); err != nil {
		t.Fatal(err)
	}
	if got := f.placed(t, a); got.ReleaseID != f.release || got.Rank != "V" || got.Revision != 3 {
		t.Fatalf("person undo=%+v", got)
	}
}

func TestStorePlanNumberOrderConversionAndRankUndo(t *testing.T) {
	f := newStoreFixture(t)
	request := PlanRequest{ProjectID: f.project, Visibility: "published", Title: "Release 3", CreationKey: "plan-3"}
	r, err := f.store.Plan(t.Context(), f.person, request)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sequence != 3 || r.Revision != 1 || r.Rank <= "D" {
		t.Fatalf("plan=%+v", r)
	}
	again, err := f.store.Plan(t.Context(), f.person, request)
	if err != nil || again.ID != r.ID || f.scalar(t, `SELECT count(*) FROM events WHERE type='release.planned'`) != 1 {
		t.Fatalf("plan replay: %+v %v", again, err)
	}
	if _, err = f.store.Plan(t.Context(), f.agent, request); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("agent plan: %v", err)
	}
	_, err = f.store.Rerank(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: f.next, ExpectedRevision: 1, Slot: Slot{BeforeID: f.release}})
	if !errors.Is(err, ErrPublishedOrder) || f.releaseRow(t, f.next).Rank != "D" {
		t.Fatalf("number inversion: %v", err)
	}
	internal, err := f.store.Plan(t.Context(), f.person, PlanRequest{ProjectID: f.project, Visibility: "internal", Title: "Audit sweep"})
	if err != nil {
		t.Fatal(err)
	}
	if internal.Sequence != 0 {
		t.Fatal("internal plan consumed a number")
	}
	moved, err := f.store.Rerank(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: internal.ID, ExpectedRevision: 1, Slot: Slot{BeforeID: f.release}})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Rank >= "B" {
		t.Fatal("internal rank did not move freely")
	}
	e := f.lastEvent(t, "release.reranked")
	if err = f.undo(t, f.person, e); err != nil {
		t.Fatal(err)
	}
	if got := f.releaseRow(t, internal.ID); got.Rank != internal.Rank || got.Revision != 3 {
		t.Fatalf("rank undo=%+v", got)
	}
	converted, err := f.store.PromoteRelease(t.Context(), f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: internal.ID, ExpectedRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	if converted.Visibility != "published" || converted.Sequence != 4 || converted.Rank <= r.Rank || converted.Revision != 4 {
		t.Fatalf("conversion=%+v", converted)
	}
	if n := f.scalar(t, `SELECT next_sequence FROM project_delivery WHERE project_node_id=$1`, f.project); n != 5 {
		t.Fatalf("high water=%d", n)
	}
}

func TestStoreFinalAdmissionAndHistoryAuthority(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		kind                     tenant.PrincipalKind
		dest                     string
		expired, history, member bool
		want                     error
	}{
		{"agent before deadline", tenant.Agent, "building", false, false, false, nil},
		{"agent at deadline", tenant.Agent, "building", true, false, false, ErrEntryClosed},
		{"person at deadline", tenant.Person, "building", true, false, false, nil},
		{"person frozen", tenant.Person, "frozen", false, false, false, ErrFrozen},
		{"agent frozen", tenant.Agent, "frozen", false, false, false, ErrFrozen},
		{"history correction", tenant.Person, "released", true, true, false, nil},
		{"member history", tenant.Person, "released", false, true, true, ErrHistoryCorrection},
		{"agent history", tenant.Agent, "released", false, true, false, ErrHistoryCorrection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStoreFixture(t)
			id := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			if tc.expired {
				f.exec(t, `UPDATE project_releases SET entry_closes_at=$2 WHERE release_node_id=$1`, f.next, f.clock)
			}
			if tc.dest == "released" {
				f.exec(t, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.next)
				f.exec(t, `UPDATE project_releases SET version='1.0.0',version_scheme='legacy',cut_at=$2 WHERE release_node_id=$1`, f.next, f.clock)
				f.exec(t, `UPDATE project_releases SET state='released',released_at=$2,reservation_basis='attested',released_by=$3 WHERE release_node_id=$1`, f.next, f.clock, f.actor)
			} else {
				f.exec(t, `UPDATE project_releases SET state=$2 WHERE release_node_id=$1`, f.next, tc.dest)
			}
			p := f.person
			if tc.kind == tenant.Agent {
				p = f.agent
			}
			if tc.member {
				dbtest.BindRole(t, f.d, f.tenant, f.actor, "member")
			}
			_, err := f.store.Place(t.Context(), p, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("admission: %v, want %v", err, tc.want)
			}
			got := f.placed(t, id)
			if tc.want == nil {
				if got.ReleaseID != f.next || got.Revision != 2 {
					t.Fatalf("placement=%+v", got)
				}
			} else if got.ReleaseID != f.release || got.Revision != 1 || f.scalar(t, `SELECT count(*) FROM events`) != 0 {
				t.Fatal("refusal wrote state or audit")
			}
		})
	}
}

func TestStoreUndoRechecksFreezeDeadlineProjectAndRevision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *storeFixture, string)
		want   error
	}{
		{"freeze", func(t *testing.T, f *storeFixture, id string) {
			f.exec(t, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.release)
		}, ErrFrozen},
		{"revision", func(t *testing.T, f *storeFixture, id string) {
			f.exec(t, `UPDATE ships_in SET revision=revision+1 WHERE item_node_id=$1`, id)
		}, ErrRevisionChanged},
		{"project", func(t *testing.T, f *storeFixture, id string) {
			f.exec(t, `DELETE FROM ships_in WHERE item_node_id=$1`, id)
			f.exec(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, f.other)
		}, ErrProjectChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStoreFixture(t)
			id := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			if _, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
				t.Fatal(err)
			}
			e := f.lastEvent(t, "ships_in.changed")
			tc.change(t, f, id)
			if err := f.undo(t, f.person, e); !errors.Is(err, tc.want) {
				t.Fatalf("undo: %v, want %v", err, tc.want)
			}
		})
	}
}

func TestStoreRolloverNearestBuildingAndTombstones(t *testing.T) {
	for _, action := range []string{"cut", "close", "abandon"} {
		t.Run(action, func(t *testing.T) {
			f := newStoreFixture(t)
			source := f.releaseRow(t, f.release)
			next := f.releaseRow(t, f.next)
			if action == "close" {
				source = f.addRelease(t, f.project, "REL-3", "internal", "planned", "F")
				next = f.addRelease(t, f.project, "REL-4", "internal", "building", "G")
				f.addRelease(t, f.project, "REL-5", "internal", "planned", "H")
			} else {
				f.exec(t, `UPDATE project_releases SET state='building' WHERE release_node_id=$1`, next.ID)
				if _, err := f.store.Plan(t.Context(), f.person, PlanRequest{ProjectID: f.project, Visibility: "published", Title: "Later"}); err != nil {
					t.Fatal(err)
				}
			}
			open := f.item(t, "ticket", "TK-1", "open", source.ID, "V")
			deleted := f.item(t, "task", "TSK-1", "open", source.ID, "W")
			done := f.item(t, "ticket", "TK-2", "done", source.ID, "X")
			cancelled := f.item(t, "ticket", "TK-3", "cancelled", source.ID, "Y")
			epic := f.item(t, "epic", "EP-1", "open", source.ID, "Z")
			f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, deleted, f.clock)
			source = f.freeze(t, source.ID)
			beforePlans := f.scalar(t, `SELECT count(*) FROM events WHERE type='release.planned'`)
			in := TransitionRequest{ProjectID: f.project, ReleaseID: source.ID, ExpectedRevision: source.Revision, Action: action, VersionScheme: "legacy", Version: "2.0.0"}
			out, err := f.store.Transition(t.Context(), f.person, in)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{open, deleted} {
				if got := f.placed(t, id); got.ReleaseID != next.ID || got.Revision != 2 {
					t.Fatalf("rollover %+v", got)
				}
			}
			if f.scalar(t, `SELECT count(*) FROM events WHERE type='release.planned'`) != beforePlans {
				t.Fatal("created successor despite nearer building release")
			}
			if action == "abandon" {
				if out.State != "abandoned" || f.scalar(t, `SELECT count(*) FROM ships_in WHERE release_node_id=$1`, source.ID) != 0 || f.placed(t, done).ReleaseID != next.ID || f.placed(t, epic).ReleaseID != next.ID || f.placed(t, cancelled).ReleaseID != "" {
					t.Fatal("abandon did not empty all rows")
				}
			} else {
				if f.placed(t, done).ReleaseID != source.ID || f.placed(t, epic).ReleaseID != source.ID || f.placed(t, cancelled).ReleaseID != source.ID {
					t.Fatal("cut/close moved completed, cancelled or tracking-only epic")
				}
				if action == "cut" && (out.CutAt == nil || !out.CutAt.Equal(f.clock) || out.Version != "2.0.0") {
					t.Fatalf("cut=%+v", out)
				}
				if action == "close" && (out.ReleasedAt == nil || !out.ReleasedAt.Equal(f.clock) || out.Version != "") {
					t.Fatalf("close=%+v", out)
				}
			}
			if _, ok := f.store.UndoHandlers()["ships_in.rolled_over"]; ok {
				t.Fatal("one-way rollover exposed undo")
			}
		})
	}
}

func TestStoreRolloverFailureIsAtomic(t *testing.T) {
	for _, mode := range []string{"deadline", "full", "empty-close"} {
		t.Run(mode, func(t *testing.T) {
			f := newStoreFixture(t)
			source, next := f.releaseRow(t, f.release), f.releaseRow(t, f.next)
			want := error(ErrEntryClosed)
			if mode == "empty-close" {
				source = f.addRelease(t, f.project, "REL-3", "internal", "planned", "F")
				next = f.addRelease(t, f.project, "REL-4", "internal", "planned", "G")
				want = ErrEmptyRelease
			}
			id := f.item(t, "ticket", "TK-1", "open", source.ID, "V")
			if mode == "deadline" {
				f.exec(t, `UPDATE project_releases SET entry_closes_at=$2 WHERE release_node_id=$1`, next.ID, f.clock)
			}
			if mode == "full" {
				f.fill(t, next.ID, 1000, "FULL")
				want = ErrReleaseCapacity
			}
			source = f.freeze(t, source.ID)
			eventsBefore := f.scalar(t, `SELECT count(*) FROM events`)
			p := f.person
			action := "cut"
			if mode == "deadline" {
				p = f.agent
			}
			if mode == "empty-close" {
				action = "close"
			}
			_, err := f.store.Transition(t.Context(), p, TransitionRequest{ProjectID: f.project, ReleaseID: source.ID, ExpectedRevision: source.Revision, Action: action, VersionScheme: "legacy", Version: "2.0.0"})
			if !errors.Is(err, want) {
				t.Fatalf("rollover error: %v, want %v", err, want)
			}
			got := f.releaseRow(t, source.ID)
			if got.State != "frozen" || got.Version != "" || got.Revision != source.Revision || f.placed(t, id).ReleaseID != source.ID || f.placed(t, id).Revision != 1 || f.scalar(t, `SELECT count(*) FROM events`) != eventsBefore {
				t.Fatal("failed transition left state, membership or events")
			}
		})
	}
}

func (f *storeFixture) fill(t *testing.T, release string, count int, prefix string) {
	t.Helper()
	ranks, err := SeedRanks(count)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state)
 SELECT $1,$2,$3||'-'||i::text,'capacity fixture',$4,'done' FROM generate_series(1,$5) i RETURNING id::text`, f.tenant, f.kinds["ticket"], prefix, f.project, count)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i, id := range ids {
			f.place(t, ctx, tx, id, release, ranks[i])
		}
		return nil
	})
}

func TestStoreCutReplayOrderVersionAndSuccessorAuthority(t *testing.T) {
	f := newStoreFixture(t)
	later := f.freeze(t, f.next)
	_, err := f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: later.ID, ExpectedRevision: later.Revision, Action: "cut", VersionScheme: "legacy", Version: "3.0.0"})
	var conflict *Conflict
	if !errors.As(err, &conflict) || conflict.Code != "cut_order" {
		t.Fatalf("cut order: %v", err)
	}
	f.item(t, "ticket", "TK-1", "open", f.release, "V")
	source := f.freeze(t, f.release)
	out, err := f.store.Transition(t.Context(), f.agent, TransitionRequest{ProjectID: f.project, ReleaseID: source.ID, ExpectedRevision: source.Revision, Action: "cut", VersionScheme: "legacy", Version: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	plan := f.lastEvent(t, "release.planned")
	if plan.ActorPrincipalID != f.agent.ID {
		t.Fatal("rollover successor attribution lost")
	}
	if f.scalar(t, `SELECT count(*) FROM project_releases WHERE sequence=3`) != 1 {
		t.Fatal("missing successor")
	}
	count := f.scalar(t, `SELECT count(*) FROM events`)
	replay, err := f.store.Transition(t.Context(), f.agent, TransitionRequest{ProjectID: f.project, ReleaseID: out.ID, ExpectedRevision: out.Revision, Action: "cut", VersionScheme: "legacy", Version: "2.0.0"})
	if err != nil || replay.Revision != out.Revision || f.scalar(t, `SELECT count(*) FROM events`) != count {
		t.Fatalf("cut replay: %+v %v", replay, err)
	}
	if _, err = f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: out.ID, ExpectedRevision: out.Revision, Action: "unfreeze"}); !errors.Is(err, ErrTransition) {
		t.Fatalf("uncut path reopened cut: %v", err)
	}
	if _, err = f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: f.project, ReleaseID: out.ID, ExpectedRevision: out.Revision, Action: "cut", VersionScheme: "legacy", Version: "3.0.0"}); !errors.Is(err, ErrTransition) {
		t.Fatalf("recut: %v", err)
	}
}

func TestStoreReleaseAndPendingCaps(t *testing.T) {
	f := newStoreFixture(t)
	f.fill(t, f.next, 1000, "FULL")
	id := f.item(t, "ticket", "TK-1", "open", f.release, "V")
	_, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}})
	if !errors.Is(err, ErrReleaseCapacity) || f.placed(t, id).ReleaseID != f.release {
		t.Fatalf("release cap: %v", err)
	}
	closed := []Release{}
	for i := 0; i < 5; i++ {
		r := f.addRelease(t, f.project, fmt.Sprintf("REL-%d", i+3), "internal", "frozen", string(rune('F'+i)))
		f.fill(t, r.ID, 800, fmt.Sprintf("PEND%d", i))
		f.exec(t, `UPDATE project_releases SET state='released',released_at=$2 WHERE release_node_id=$1`, r.ID, f.clock)
		closed = append(closed, r)
	}
	_, err = f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: closed[0].ID, ExpectedReleaseRevision: 1}})
	if !errors.Is(err, ErrPendingCapacity) || f.placed(t, id).Revision != 1 || f.scalar(t, `SELECT count(*) FROM events`) != 0 {
		t.Fatalf("history pending cap: %v", err)
	}
	// Exactly 4,000 pending rows still permit removals. Undo of that removal
	// must refuse after another correction fills the freed slot.
	var pendingItem string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT item_node_id::text FROM ships_in WHERE release_node_id=$1 LIMIT 1`, closed[0].ID).Scan(&pendingItem)
	})
	if _, err = f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: pendingItem, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
		t.Fatal(err)
	}
	removal := f.lastEvent(t, "ships_in.changed")
	if _, err = f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: closed[0].ID, ExpectedReleaseRevision: 2}}); err != nil {
		t.Fatal(err)
	}
	if err = f.undo(t, f.person, removal); !errors.Is(err, ErrPendingCapacity) {
		t.Fatalf("undo pending cap: %v", err)
	}
}

func TestStoreExpediteAndBoundedInput(t *testing.T) {
	f := newStoreFixture(t)
	stale := f.item(t, "ticket", "TK-1", "done", f.release, "V")
	id := f.item(t, "ticket", "TK-2", "open", f.release, "W")
	f.exec(t, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, stale)
	date := "2026-10-05"
	if _, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1, Expedite: true, DueOn: &date}}); err != nil {
		t.Fatal(err)
	}
	if f.placed(t, stale).Expedite || !f.placed(t, id).Expedite || !sameDate(f.placed(t, id).DueOn, &date) || f.placed(t, stale).Revision != 2 {
		t.Fatal("stale expedite not cleared atomically")
	}
	if _, err := f.store.Place(t.Context(), f.person, f.project, make([]PlacementRequest, 101)); err == nil {
		t.Fatal("accepted oversized batch")
	}
	if _, err := f.store.Plan(t.Context(), f.person, PlanRequest{ProjectID: f.project, Visibility: "internal", Title: strings.Repeat("a", 513)}); err == nil {
		t.Fatal("accepted oversized title")
	}
	if _, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.other, ExpectedRevision: 2}}); !errors.Is(err, ErrProjectChanged) {
		t.Fatalf("project CAS: %v", err)
	}
}

func TestProjectVersionTaggedGrammar(t *testing.T) {
	for _, tc := range []struct {
		scheme, version string
		valid           bool
	}{
		{"legacy", "v1.2.3-rc.1+build", true}, {"legacy", "261003120000.0.0", true}, {"inspr-calendar-v1", "26.10.03", true}, {"inspr-calendar-v1", "26.10.03.12.30.01", true},
		{"inspr-calendar-v1", "26.02.30", false}, {"inspr-calendar-v2", "261003120000.0.0", true}, {"inspr-calver-3", "261003120000.0.0", true}, {"unknown", "1.0.0", false}, {"legacy", strings.Repeat("a", 65), false}, {"legacy", "/bad", false},
	} {
		if got := ValidProjectVersion(tc.scheme, tc.version); got != tc.valid {
			t.Errorf("%s %s=%v, want %v", tc.scheme, tc.version, got, tc.valid)
		}
	}
}

// The publication callback stands in for P4a's project-bound note producer.
// It selects/locks its real unit, checks completion and writes its capture in
// the same transaction. P2 does not claim reservation/notes decoder coverage.
func captureUnit(id, key string) PreparePublication {
	return func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Release) (Publication, error) {
		var state string
		if err := tx.QueryRow(ctx, `SELECT n.state FROM nodes n JOIN ships_in s ON s.tenant_id=n.tenant_id AND s.item_node_id=n.id WHERE n.tenant_id=$1 AND n.project_id=$2 AND n.id=$3 AND s.release_node_id=$4 FOR SHARE OF n`, p.TenantID, r.ProjectID, id, r.ID).Scan(&state); err != nil {
			return Publication{}, err
		}
		if !Completed(state) {
			return Publication{}, ErrEmptyRelease
		}
		return Publication{Basis: "attested", Reference: "https://example.invalid/reservation", Capture: func(ctx context.Context, tx pgx.Tx, published Release) error {
			snapshot := map[string]any{"schema": "aeon.release-note-snapshot.v1", "tenant_id": p.TenantID, "project_node_id": r.ProjectID, "release_node_id": r.ID, "version": r.Version, "version_scheme": r.VersionScheme, "release_revision": published.Revision, "captured_at": published.ReleasedAt, "membership_source": "ships_in.release_node_id", "field_source": "nodes.fields", "frozen": true, "tickets": []any{map[string]any{"id": id, "key": key}}}
			body, err := json.Marshal(snapshot)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,$4,$5::jsonb)`, p.TenantID, r.ProjectID, r.ID, r.Version, body)
			return err
		}}, nil
	}
}
