// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"testing"
)

func TestDatabasesAreIsolated(t *testing.T) {
	ctx := context.Background()
	// Risk: reusing an expensive seed must preserve fixture data, independent
	// writes, FORCE RLS and ownership of SECURITY DEFINER functions in clones.
	source := Open(t)
	if _, err := source.Admin.Exec(ctx, `INSERT INTO tenants(slug,name) VALUES('seed','Seed')`); err != nil {
		t.Fatal(err)
	}
	source.App.Close()
	source.Admin.Close()
	clone := func() *DB {
		d, err := Clone(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reportCleanup(t, d.Close()) })
		var count int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM tenants WHERE slug='seed'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("clone lost seeded tenant: count=%d err=%v", count, err)
		}
		if err := d.App.QueryRow(ctx, `SELECT count(*) FROM principals`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("clone leaked principals without tenant context: count=%d err=%v", count, err)
		}
		return d
	}
	a := clone()
	b := clone()
	if a.Name == b.Name || a.Role == b.Role || a.URL == b.URL {
		t.Fatalf("handles overlap name=%s/%s role=%s/%s", a.Name, b.Name, a.Role, b.Role)
	}

	var got string
	if err := a.Admin.QueryRow(ctx, `SELECT current_database()`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != a.Name {
		t.Fatalf("admin database %s, want %s", got, a.Name)
	}

	var user string
	var super, bypass bool
	if err := a.App.QueryRow(ctx, `
		SELECT current_user, rolsuper, rolbypassrls
		FROM pg_roles WHERE rolname = current_user`).Scan(&user, &super, &bypass); err != nil {
		t.Fatal(err)
	}
	if user != a.Role || super || bypass {
		t.Fatalf("app role %s super=%v bypass=%v", user, super, bypass)
	}
	var jit string
	if err := a.App.QueryRow(ctx, `SHOW jit`).Scan(&jit); err != nil {
		t.Fatal(err)
	}
	if jit != "off" {
		t.Fatalf("test app JIT is %s, want off like db.Open", jit)
	}

	var owner string
	if err := a.Admin.QueryRow(ctx, `
		SELECT tableowner FROM pg_tables
		WHERE schemaname = 'public' AND tablename = 'principals'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != a.Role {
		t.Fatalf("principals owner %s, want app role %s", owner, a.Role)
	}
	// DDL ownership includes SECURITY DEFINER functions and sequences, not
	// merely tables. Extension members remain owned by the bootstrap user.
	var foreignObjects int
	if err := a.Admin.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT c.oid, 'pg_class'::regclass AS classid, c.relowner AS owner FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname='public' AND c.relkind IN ('r','p','S')
			UNION ALL
			SELECT p.oid, 'pg_proc'::regclass, p.proowner FROM pg_proc p
			JOIN pg_namespace n ON n.oid=p.pronamespace
			WHERE n.nspname='public'
		) objects
		WHERE owner <> $1::regrole
		AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                WHERE d.classid=objects.classid AND d.objid=objects.oid AND d.deptype='e')`, a.Role).Scan(&foreignObjects); err != nil {
		t.Fatal(err)
	}
	if foreignObjects != 0 {
		t.Fatalf("%d application tables, sequences or functions are not owned by app role", foreignObjects)
	}

	if _, err := a.Admin.Exec(ctx, `INSERT INTO tenants (slug, name) VALUES ('iso', 'Iso')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.Admin.QueryRow(ctx, `SELECT count(*) FROM tenants WHERE slug = 'iso'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("tenant inserted in one database is visible in the other")
	}
}
