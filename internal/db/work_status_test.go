// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type statusFixture struct {
	t   *testing.T
	d   *dbtest.DB
	tid string
}

func newStatusFixture(t *testing.T) *statusFixture {
	t.Helper()
	d := dbtest.Open(t)
	f := &statusFixture{t: t, d: d}
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('work-status','Work status') RETURNING id::text`).Scan(&f.tid); err != nil {
		t.Fatal(err)
	}
	// The dependency is not yet stacked here. This is the exact AEON-429 storage
	// contract, not a substitute production flag implementation.
	if _, err := d.App.Exec(t.Context(), `CREATE TABLE IF NOT EXISTS features(
 tenant_id uuid NOT NULL REFERENCES tenants(id),key text NOT NULL,project_id uuid,
 enabled boolean DEFAULT false,revision bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE NULLS NOT DISTINCT(tenant_id,key,project_id),FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id));
 ALTER TABLE features ENABLE ROW LEVEL SECURITY; ALTER TABLE features FORCE ROW LEVEL SECURITY;
 CREATE POLICY status_fixture_tenant ON features USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
 CREATE POLICY status_fixture_project ON features AS RESTRICTIVE USING(project_id IS NULL OR aeon_visible_all() OR project_id=ANY(aeon_visible_projects()))`); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,'PRJ-1','Project' FROM node_kinds k WHERE slug='project'`, f.tid)
	return f
}
func (f *statusFixture) tx(fn func(pgx.Tx) error) error {
	return db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.tid, fn)
}
func (f *statusFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if err := f.tx(func(tx pgx.Tx) error { _, err := tx.Exec(f.t.Context(), sql, args...); return err }); err != nil {
		f.t.Fatal(err)
	}
}
func (f *statusFixture) node(key, parent, state string) {
	f.t.Helper()
	f.exec(`INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id) SELECT $1,k.id,$2,$2,$3,p.id FROM node_kinds k,nodes p WHERE k.slug='work' AND p.key=$4`, f.tid, key, state, parent)
}
func (f *statusFixture) enable() {
	f.t.Helper()
	f.exec(`SELECT aeon_authz_system_actor($1)`, f.tid)
	f.exec(`INSERT INTO features(tenant_id,key,enabled) VALUES($1,'work-parent-status',true) ON CONFLICT(tenant_id,key,project_id) DO UPDATE SET enabled=true`, f.tid)
}
func (f *statusFixture) state(key string) string {
	f.t.Helper()
	var got string
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT state FROM nodes WHERE key=$1`, key).Scan(&got)
	}); err != nil {
		f.t.Fatal(err)
	}
	return got
}
func (f *statusFixture) check(key, want string) {
	f.t.Helper()
	if got := f.state(key); got != want {
		f.t.Fatalf("%s state %s, want %s", key, got, want)
	}
}
func (f *statusFixture) change(key, state string) {
	f.t.Helper()
	f.exec(`UPDATE nodes SET state=$2,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE key=$1`, key, state)
}

func TestWorkStatusRulesAndCategories(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "new")
	f.node("WK-2", "WK-1", "open")
	f.node("WK-3", "WK-1", "new")
	f.enable()
	for _, tc := range []struct{ a, b, want string }{
		{"new", "backlog", "open"}, {"blocked", "open", "blocked"}, {"qa", "blocked", "in_progress"},
		{"in_progress", "done", "in_progress"}, {"done", "cancelled", "done"}, {"cancelled", "canceled", "cancelled"},
		{"accepted", "delivered", "delivered"}, {"accepted", "done", "done"}, {"accepted", "accepted", "accepted"},
		{"delivered", "cancelled", "done"}, {"archived", "accepted", "accepted"}, {"archived", "archived", "accepted"},
	} {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			f.exec(`UPDATE nodes SET state=CASE key WHEN 'WK-2' THEN $1 ELSE $2 END,updated_at=clock_timestamp() WHERE key IN ('WK-2','WK-3')`, tc.a, tc.b)
			f.check("WK-1", tc.want)
		})
	}
	f.exec(`UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{states}','[{"state":"shipping","category":"doing"},{"state":"waiting","category":"blocked"},{"state":"ignored","category":"archived"}]') WHERE slug='work'`)
	f.change("WK-2", "shipping")
	f.change("WK-3", "waiting")
	f.check("WK-1", "in_progress")
	f.exec(`UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{states}','[{"state":"shipping","category":"done"},{"state":"waiting","category":"archived"}]') WHERE slug='work'`)
	f.check("WK-1", "done")
}
func TestWorkStatusMovesDeletionRestoreAndRevisions(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "open")
	f.node("WK-4", "PRJ-1", "open")
	f.node("WK-2", "WK-1", "open")
	f.node("WK-3", "WK-1", "open")
	f.node("WK-5", "WK-4", "open")
	f.enable()
	var rev time.Time
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE key='WK-1'`).Scan(&rev)
	}); err != nil {
		t.Fatal(err)
	}
	f.change("WK-2", "in_progress")
	f.check("WK-1", "in_progress")
	var next time.Time
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE key='WK-1'`).Scan(&next)
	}); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(rev) {
		t.Fatal("derived status changed parent edit revision")
	}
	f.exec(`UPDATE nodes SET parent_id=(SELECT id FROM nodes WHERE key='WK-4'),updated_at=clock_timestamp() WHERE key='WK-2'`)
	f.check("WK-1", "open")
	f.check("WK-4", "in_progress")
	f.exec(`UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE key='WK-2'`)
	f.check("WK-4", "open")
	f.exec(`UPDATE nodes SET deleted_at=NULL,updated_at=clock_timestamp() WHERE key='WK-2'`)
	f.check("WK-4", "in_progress")
	f.exec(`UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE key IN ('WK-2','WK-5')`)
	f.check("WK-4", "in_progress")
	var count int
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=(SELECT id FROM nodes WHERE key='WK-4') AND type='status_autopilot.retained'`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("last-child retention events %d", count)
	}
	// A non-work child does not make a parent.
	f.exec(`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'MEM-1','Memory',p.id FROM node_kinds k,nodes p WHERE k.slug='memory' AND p.key='WK-3'`, f.tid)
	f.change("WK-3", "done")
	f.check("WK-1", "done")
}
func TestWorkStatusBoundaryFlagAndCoalescing(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "open")
	f.node("WK-2", "WK-1", "open")
	f.node("WK-3", "WK-1", "open")
	f.change("WK-2", "done")
	f.check("WK-1", "open") // default OFF
	f.enable()
	err := f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE key='WK-1'`)
		return err
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "P0001" || pg.Message != "parent status follows its children" {
		t.Fatalf("parent guard: %v", err)
	}
	err = db.InTransaction(dbtest.Seed(t.Context()), f.d.App, func(ctx context.Context) error {
		for _, state := range []string{"blocked", "in_progress", "accepted"} {
			if err := db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE nodes SET state=$1,updated_at=clock_timestamp() WHERE key IN ('WK-2','WK-3')`, state)
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.check("WK-1", "accepted")
	var count int
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='status_autopilot.derived'`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("coalesced transitions %d want 1", count)
	}
	// Project OFF overrides tenant ON, and the trigger restores caller visibility.
	f.exec(`INSERT INTO features(tenant_id,key,project_id,enabled) SELECT $1,'work-parent-status',id,false FROM nodes WHERE key='PRJ-1'`, f.tid)
	f.change("WK-2", "open")
	f.check("WK-1", "accepted")
	f.change("WK-1", "blocked")
	f.check("WK-1", "blocked")
}
func TestWorkStatusTenantVisibilityRestored(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "open")
	f.node("WK-2", "WK-1", "open")
	f.enable()
	// A restricted RLS context sees no projects before or after the narrow engine step.
	err := db.InTenant(db.NoProjects(t.Context(), "visibility regression"), f.d.App, f.tid, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("system authority leaked: %d nodes", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkStatusConcurrentWritersUseCommittedTree(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "open")
	f.node("WK-2", "WK-1", "open")
	f.node("WK-3", "WK-1", "open")
	f.enable()
	ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 15*time.Second)
	defer cancel()
	locked := make(chan uint32, 1)
	release := make(chan struct{})
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		first <- db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
			var pid uint32
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET state='done' WHERE key='WK-2'`); err != nil {
				return err
			}
			locked <- pid
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	var blocker uint32
	select {
	case blocker = <-locked:
	case err := <-first:
		t.Fatalf("first writer: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		second <- db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE nodes SET state='cancelled' WHERE key='WK-3'`)
			return err
		})
	}()
	// Observe the actual database wait, not just a goroutine starting or elapsed
	// time. Only then release the first writer and inspect the committed result.
	for {
		select {
		case err := <-second:
			t.Fatalf("second writer escaped barrier: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		var waiting bool
		if err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND $1::int=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	close(release)
	for _, ch := range []<-chan error{first, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	f.check("WK-1", "done")
	var transitions int
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='status_autopilot.derived'`).Scan(&transitions)
	}); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("real transitions %d, want 1", transitions)
	}
}

func TestWorkStatusDeepAndWideBoundedCascade(t *testing.T) {
	f := newStatusFixture(t)
	parent := "PRJ-1"
	for i := 1; i <= 64; i++ {
		key := fmt.Sprintf("WK-%d", i)
		f.node(key, parent, "open")
		parent = key
	}
	f.exec(`INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id)
  SELECT $1,k.id,'WK-'||(1000+s), 'Leaf','open',p.id FROM node_kinds k,nodes p,generate_series(1,300) s WHERE k.slug='work' AND p.key='WK-64'`, f.tid)
	f.enable()
	f.exec(`UPDATE nodes SET state='done' WHERE key LIKE 'WK-1___'`)
	f.check("WK-1", "done")
	f.check("WK-64", "done")
	var count int
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='status_autopilot.derived'`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 64 {
		t.Fatalf("cascade transitions %d, want 64", count)
	}
}

func TestWorkStatusRejectsOversizedChangesAtomically(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "open")
	f.exec(`INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id)
 SELECT $1,k.id,'WK-'||(1000+s),'Leaf','open',p.id FROM node_kinds k,nodes p,generate_series(1,1001) s WHERE k.slug='work' AND p.key='WK-1'`, f.tid)
	f.enable()
	err := f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE parent_id=(SELECT id FROM nodes WHERE key='WK-1')`)
		return err
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "54000" || pg.Message != "work status change scope exceeds 1000 nodes" {
		t.Fatalf("wrong bound refusal %v", err)
	}
	f.check("WK-1", "open")
	var changed int
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE state='done'`).Scan(&changed)
	}); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("partial oversized write %d", changed)
	}
}

// Use the production SQL functions unchanged except for their literal capacity
// in this isolated database. A three-row boundary exercises the same guards
// without inserting 50,000 rows on the shared workstation.
func (f *statusFixture) smallLimit() {
	f.t.Helper()
	for _, name := range []string{"aeon_work_status_begin", "aeon_work_status_flush"} {
		var definition string
		if err := f.d.App.QueryRow(f.t.Context(), `SELECT pg_get_functiondef(($1||'()')::regprocedure)`, name).Scan(&definition); err != nil {
			f.t.Fatal(err)
		}
		definition = strings.ReplaceAll(strings.ReplaceAll(definition, "LIMIT 50001", "LIMIT 4"), "total>50000", "total>3")
		if _, err := f.d.App.Exec(f.t.Context(), definition); err != nil {
			f.t.Fatal(err)
		}
	}
}
func TestWorkStatusNodeLimitCrossingRollsBack(t *testing.T) {
	f := newStatusFixture(t)
	f.smallLimit()
	for i := 1; i <= 3; i++ {
		f.node(fmt.Sprintf("WK-%d", i), "PRJ-1", "open")
	}
	f.enable()
	err := f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'WK-4','Crossing',p.id FROM node_kinds k,nodes p WHERE k.slug='work' AND p.key='PRJ-1'`, f.tid)
		return err
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "54000" || pg.Message != "work status scope exceeds 50000 nodes" {
		t.Fatalf("limit-crossing mutation did not fail atomically: %v", err)
	}
	var count int
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE k.slug='work'`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("partial crossing write: %d", count)
	}
	f.check("WK-1", "open")
	f.exec(`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'MEM-1','Memory',p.id FROM node_kinds k,nodes p WHERE k.slug='memory' AND p.key='PRJ-1'`, f.tid)
	err = db.InTransaction(dbtest.Seed(t.Context()), f.d.App, func(ctx context.Context) error {
		// The early write must roll back too when a later kind conversion
		// exceeds capacity at the grouped transaction's final boundary.
		if err := db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE nodes SET title='Must roll back' WHERE key='WK-1'`)
			return err
		}); err != nil {
			return err
		}
		return db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE slug='work') WHERE key='MEM-1'`)
			return err
		})
	})
	if !errors.As(err, &pg) || pg.Code != "54000" || pg.Message != "work status scope exceeds 50000 nodes" {
		t.Fatalf("grouped kind conversion did not fail atomically: %v", err)
	}
	if err := f.tx(func(tx pgx.Tx) error {
		var title, kind string
		if err := tx.QueryRow(t.Context(), `SELECT title FROM nodes WHERE key='WK-1'`).Scan(&title); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.key='MEM-1'`).Scan(&kind); err != nil {
			return err
		}
		if title != "WK-1" || kind != "memory" {
			return fmt.Errorf("partial grouped write: title=%s kind=%s", title, kind)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestWorkStatusNodeLimitRecoveryAndTombstones(t *testing.T) {
	t.Run("over-limit read and disable", func(t *testing.T) {
		f := newStatusFixture(t)
		f.smallLimit()
		for i := 1; i <= 4; i++ {
			f.node(fmt.Sprintf("WK-%d", i), "PRJ-1", "open")
		}
		f.enable() // An old/external activation may already exceed capacity.
		f.check("WK-1", "open")
		f.exec(`UPDATE features SET enabled=false WHERE tenant_id=$1`, f.tid)
		f.change("WK-1", "done")
		f.check("WK-1", "done")
	})
	t.Run("tombstones do not consume capacity; restore does", func(t *testing.T) {
		f := newStatusFixture(t)
		f.smallLimit()
		for i := 1; i <= 4; i++ {
			f.node(fmt.Sprintf("WK-%d", i), "PRJ-1", "open")
		}
		f.exec(`UPDATE nodes SET deleted_at=clock_timestamp() WHERE key='WK-4'`)
		f.enable()
		f.check("WK-1", "open")
		err := f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=NULL WHERE key='WK-4'`)
			return err
		})
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "54000" || pg.Message != "work status scope exceeds 50000 nodes" {
			t.Fatalf("restore crossing limit: %v", err)
		}
		var deleted bool
		if err := f.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM nodes WHERE key='WK-4'`).Scan(&deleted)
		}); err != nil {
			t.Fatal(err)
		}
		if !deleted {
			t.Fatal("restore partially committed")
		}
	})
}
