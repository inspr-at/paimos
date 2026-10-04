// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"errors"
	"fmt"
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
