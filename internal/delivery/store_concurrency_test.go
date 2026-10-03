// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func cutRelease(t *testing.T, f *storeFixture, r Release) Release {
	t.Helper()
	frozen := f.freeze(t, r.ID)
	cut, err := f.store.Transition(t.Context(), f.person, TransitionRequest{ProjectID: r.ProjectID, ReleaseID: r.ID, ExpectedRevision: frozen.Revision, Action: "cut", VersionScheme: "legacy", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	return cut
}

func TestStoreTwoPublicationsExclusiveFromStart(t *testing.T) {
	f := newStoreFixture(t)
	unitA := f.item(t, "ticket", "TK-1", "done", f.release, "V")
	releaseA := cutRelease(t, f, f.releaseRow(t, f.release))
	releaseB, err := f.store.Plan(t.Context(), f.person, PlanRequest{ProjectID: f.other, Visibility: "published", Title: "Other release"})
	if err != nil {
		t.Fatal(err)
	}
	var unitB string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		unitB = f.node(t, ctx, tx, "task", "TSK-1", f.other)
		if _, err := tx.Exec(ctx, `UPDATE nodes SET state='done',updated_at=clock_timestamp() WHERE id=$1`, unitB); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by) VALUES($1,$2,$3,$4,'V','person',$5)`, f.tenant, f.other, unitB, releaseB.ID, f.actor)
		return err
	})
	releaseB = cutRelease(t, f, releaseB)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool { return strings.Contains(query, "pg_advisory_xact_lock") })
	store := NewStore(pool).WithClock(func() time.Time { return f.clock })
	type result struct {
		release Release
		err     error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() {
		r, err := store.Publish(ctx, f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: releaseA.ID, ExpectedRevision: releaseA.Revision}, captureUnit(unitA, "TK-1"))
		first <- result{r, err}
	}()
	pid := barrier.Wait(t, ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := store.Publish(ctx, f.person, ReleaseEdit{ProjectID: f.other, ReleaseID: releaseB.ID, ExpectedRevision: releaseB.Revision}, captureUnit(unitB, "TSK-1"))
		second <- result{r, err}
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock != "advisory" {
		t.Fatalf("second publication must wait on first's exclusive tree: %q", lock)
	}
	probe, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback(context.Background())
	if _, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, f.tenant); err != nil {
		t.Fatalf("tree waiters acquired tenant early: %v", err)
	}
	if err = probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	for _, ch := range []<-chan result{first, second} {
		result := dbtest.Await(t, ctx, ch)
		if result.err != nil || result.release.State != "released" {
			t.Fatalf("publication: %+v %v", result.release, result.err)
		}
	}
	if n := f.scalar(t, `SELECT count(*) FROM project_release_note_snapshots WHERE release_node_id=ANY($1::uuid[])`, []string{releaseA.ID, releaseB.ID}); n != 2 {
		t.Fatalf("captures=%d", n)
	}
	if n := f.scalar(t, `SELECT count(*) FROM outcome_events WHERE kind='released' AND ticket_node_id=ANY($1::uuid[])`, []string{unitA, unitB}); n != 2 {
		t.Fatalf("unit outcomes=%d", n)
	}
	if n := f.scalar(t, `SELECT count(*) FROM project_release_note_snapshots WHERE snapshot->>'release_revision' IN ($1,$2)`, fmtRevision(releaseA.Revision+1), fmtRevision(releaseB.Revision+1)); n != 2 {
		t.Fatal("captures did not bind final release revisions")
	}
}

func fmtRevision(n int64) string { return strconv.FormatInt(n, 10) }

func TestStorePlacementWaitsForPublicationAndRechecksRevision(t *testing.T) {
	f := newStoreFixture(t)
	id := f.item(t, "ticket", "TK-1", "done", f.release, "V")
	release := cutRelease(t, f, f.releaseRow(t, f.release))
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool { return strings.Contains(query, "pg_advisory_xact_lock") })
	store := NewStore(pool).WithClock(func() time.Time { return f.clock })
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := store.Publish(ctx, f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: f.release, ExpectedRevision: release.Revision}, captureUnit(id, "TK-1"))
		first <- err
	}()
	pid := barrier.Wait(t, ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := store.Place(ctx, f.person, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: release.Revision}})
		second <- err
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock != "advisory" {
		t.Fatalf("placement did not wait at tree: %q", lock)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, second); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("stale destination after publication: %v", err)
	}
	if f.placed(t, id).Revision != 1 || f.scalar(t, `SELECT count(*) FROM project_release_note_snapshots WHERE release_node_id=$1`, f.release) != 1 {
		t.Fatal("stale placement changed publication")
	}
}

func TestStoreFinalRequireTxAfterRevocationFence(t *testing.T) {
	for _, kind := range []string{"workspace", "project"} {
		t.Run(kind, func(t *testing.T) {
			f := newStoreFixture(t)
			id := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			// A custom permission can be revoked without changing the principal,
			// key scopes, creator or visible project snapshot carried by the call.
			var role string
			f.run(t, func(ctx context.Context, tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'release_worker','Release worker') RETURNING id::text`, f.tenant).Scan(&role); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'releases.write')`, f.tenant, role); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.agent.ID); err != nil {
					return err
				}
				if kind == "workspace" {
					_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.tenant, f.agent.ID, role)
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.tenant, f.agent.ID, role, f.project)
				return err
			})
			if err := authz.Require(tenant.WithPrincipal(authz.BindPool(t.Context(), f.d.App), f.agent), "releases.write", authz.Scope{ProjectID: f.project}); err != nil {
				t.Fatalf("precondition authority absent: %v", err)
			}
			pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool {
				return strings.Contains(query, "FROM tenants") && strings.Contains(query, "FOR UPDATE")
			})
			revoke, writeDone := make(chan error, 1), make(chan error, 1)
			go func() {
				revoke <- db.InTenant(dbtest.Seed(ctx), pool, f.tenant, func(tx pgx.Tx) error {
					if kind == "project" {
						if err := authz.LockProjectMutation(ctx, tx, f.tenant); err != nil {
							return err
						}
					} else {
						var tenantID string
						if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR UPDATE`, f.tenant).Scan(&tenantID); err != nil {
							return err
						}
					}
					_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='releases.write'`, f.tenant, role)
					return err
				})
			}()
			pid := barrier.Wait(t, ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, err := f.store.Place(ctx, f.agent, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}})
				writeDone <- err
			}()
			lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done)
			if kind == "project" && lock != "advisory" || kind == "workspace" && lock != "transactionid" && lock != "tuple" {
				t.Fatalf("write bypassed authority fence: %q", lock)
			}
			barrier.Release()
			if err := dbtest.Await(t, ctx, revoke); err != nil {
				t.Fatal(err)
			}
			if err := dbtest.Await(t, ctx, writeDone); !errors.Is(err, authz.ErrForbidden) {
				t.Fatalf("stale-authority write: %v", err)
			}
			if f.placed(t, id).ReleaseID != f.release || f.placed(t, id).Revision != 1 || f.scalar(t, `SELECT count(*) FROM events`) != 0 {
				t.Fatal("revoked write persisted effects")
			}
		})
	}
}
