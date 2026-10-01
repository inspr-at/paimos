// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestClonesKeepTemplateMigrations(t *testing.T) {
	a := Open(t)
	b := Open(t)
	template, err := migratedTemplate(t.Context(), a.maint)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), a.maint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var connections, login, super, bypass bool
	if err := conn.QueryRow(t.Context(), `
		SELECT d.datallowconn, r.rolcanlogin, r.rolsuper, r.rolbypassrls
		FROM pg_database d, pg_roles r WHERE d.datname=$1 AND r.rolname=$2`,
		template.Name, template.Role).Scan(&connections, &login, &super, &bypass); err != nil {
		t.Fatal(err)
	}
	if connections || login || super || bypass {
		t.Fatalf("template connections=%v login=%v super=%v bypass=%v", connections, login, super, bypass)
	}
	const migrations = `SELECT string_agg(version || ':' || applied_at::text, ',' ORDER BY version) FROM schema_migrations`
	var first, second string
	if err := a.Admin.QueryRow(t.Context(), migrations).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := b.Admin.QueryRow(t.Context(), migrations).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatal("clones did not inherit the same migration records and timestamps")
	}
	if _, err := a.App.Exec(t.Context(), `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	c := Open(t)
	if err := c.Admin.QueryRow(t.Context(), migrations).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("changing a clone changed the template")
	}
	// A copied schema ACL must not keep a dependency on the template role.
	var dependencies int
	if err := c.Admin.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_shdepend
		WHERE refclassid='pg_authid'::regclass AND refobjid=$1::regrole
		AND dbid=(SELECT oid FROM pg_database WHERE datname=current_database())`,
		template.Role).Scan(&dependencies); err != nil {
		t.Fatal(err)
	}
	if dependencies != 0 {
		t.Fatalf("clone retains %d dependencies on template role", dependencies)
	}
}

func TestParallelClones(t *testing.T) {
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			d := Open(t)
			if _, err := d.Admin.Exec(t.Context(), `INSERT INTO tenants(slug,name) VALUES ('parallel', 'Parallel')`); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM tenants WHERE slug='parallel'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("parallel clone has %d fixture tenants, want 1", n)
			}
		})
	}
}

func TestNewUnmigratedStillHasNoSchema(t *testing.T) {
	d, err := NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tables, extensions int
	if err := d.Admin.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM pg_tables WHERE schemaname='public'),
		       (SELECT count(*) FROM pg_extension WHERE extname='vector')`).Scan(&tables, &extensions); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || extensions != 1 {
		t.Fatalf("unmigrated database has tables=%d vector extensions=%d", tables, extensions)
	}
}

func TestTemplateCleanupWaitsForLease(t *testing.T) {
	d, err := NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The binary-exit watcher must force-drop a template even if a connection
	// outside its pools lingers after the lease ends.
	lingering, err := pgx.Connect(t.Context(), d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer lingering.Close(context.Background())
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	d.App.Close()
	d.Admin.Close()
	lease, err := watchTemplate(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	conn, err := pgx.Connect(t.Context(), d.maint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	const resources = `SELECT
		EXISTS(SELECT 1 FROM pg_database WHERE datname=$1),
		EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$2)`
	var database, role bool
	if err := conn.QueryRow(t.Context(), resources, d.Name, d.Role).Scan(&database, &role); err != nil {
		t.Fatal(err)
	}
	if !database || !role {
		t.Fatal("cleanup removed live template resources")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := conn.QueryRow(t.Context(), resources, d.Name, d.Role).Scan(&database, &role); err != nil {
			t.Fatal(err)
		}
		if !database && !role {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup left database=%v role=%v after closing lease", database, role)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
