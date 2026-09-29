// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestEveryTenantTableHasForcedRLS(t *testing.T) {
	database := dbtest.Open(t)
	rows, err := database.Admin.Query(t.Context(), `
		SELECT c.relname,
		       EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attname='tenant_id' AND NOT a.attisdropped),
		       c.relrowsecurity,c.relforcerowsecurity,
		       (SELECT count(*) FROM pg_policy p WHERE p.polrelid=c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		var hasTenant, enabled, forced bool
		var policies int
		if err := rows.Scan(&name, &hasTenant, &enabled, &forced, &policies); err != nil {
			t.Fatal(err)
		}
		if name == "tenants" || name == "identities" || name == "schema_migrations" {
			continue
		}
		count++
		if !hasTenant || !enabled || !forced || policies == 0 {
			t.Errorf("%s: tenant_id=%t RLS=%t FORCE=%t policies=%d", name, hasTenant, enabled, forced, policies)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count < 50 {
		t.Fatalf("audited only %d tenant tables", count)
	}
}

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrationNames(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no embedded migrations")
	}
	return names
}

func TestMigrationsApplyAndReapply(t *testing.T) {
	ctx := context.Background()
	fresh := dbtest.Open(t)
	if err := fresh.Admin.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.Open(ctx, fresh.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)

	names := migrationNames(t)
	var n int
	if err := again.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(names) {
		t.Fatalf("schema_migrations rows = %d, want %d (one per embedded migration)", n, len(names))
	}

	var versions string
	if err := again.QueryRow(ctx, `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != strings.Join(names, ",") {
		t.Fatalf("versions %s, want %s", versions, strings.Join(names, ","))
	}

	var vector int
	if err := again.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'vector'`).Scan(&vector); err != nil {
		t.Fatal(err)
	}
	if vector != 1 {
		t.Fatal("vector extension missing")
	}

	var rls, force bool
	if err := again.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.principals'::regclass`).Scan(&rls, &force); err != nil {
		t.Fatal(err)
	}
	if !rls || !force {
		t.Fatalf("rls=%v force=%v", rls, force)
	}

	var qual, check string
	if err := again.QueryRow(ctx, `
		SELECT pg_get_expr(polqual, polrelid), pg_get_expr(polwithcheck, polrelid)
		FROM pg_policy
		WHERE polrelid = 'public.principals'::regclass AND polname = 'tenant_isolation'`).Scan(&qual, &check); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qual, "aeon.tenant_id") || !strings.Contains(check, "aeon.tenant_id") {
		t.Fatalf("policy qual=%s check=%s", qual, check)
	}

	var tenantID string
	if err := again.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('p02-ddl', 'DDL') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}

	var roles []string
	if err := again.QueryRow(ctx, `
		INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, 'person', 'defaults')
		RETURNING roles`, tenantID).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if len(roles) != 0 {
		t.Fatalf("default roles %#v", roles)
	}
	if _, err := again.Exec(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, 'robot', 'nope')`, tenantID); err == nil {
		t.Fatal("expected kind check to reject robot")
	}

	var ident string
	if err := again.QueryRow(ctx, `
		INSERT INTO identities (issuer, subject) VALUES ('p02-ddl', 'sub') RETURNING id::text`).Scan(&ident); err != nil {
		t.Fatal(err)
	}
	if _, err := again.Exec(ctx, `
		INSERT INTO principals (tenant_id, kind, name, identity_id) VALUES ($1::uuid, 'person', 'one', $2::uuid)`,
		tenantID, ident); err != nil {
		t.Fatal(err)
	}
	if _, err := again.Exec(ctx, `
		INSERT INTO principals (tenant_id, kind, name, identity_id) VALUES ($1::uuid, 'agent', 'two', $2::uuid)`,
		tenantID, ident); err == nil {
		t.Fatal("expected partial unique index to reject a second identity link")
	}
	if _, err := again.Exec(ctx, `
		INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, 'agent', 'null-identity')`, tenantID); err != nil {
		t.Fatal(err)
	}
}

// Session thread walks filter sender_session_id and join reply_to_id under the
// caller's row-level security (AEON-345). recipient_session_id already had an index.
func TestSessionThreadLookupIndexes(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tenantID string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('thread-idx','Thread indexes') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
			return err
		}
		project := "00000000-0000-4000-8000-000000000001"
		session := "00000000-0000-4000-8000-000000000002"
		checks := []struct {
			name  string
			query string
			args  []any
		}{
			{"inbox_compat_sender_session", `EXPLAIN SELECT id FROM inbox_compat_messages WHERE project_id=$1 AND sender_session_id=$2`, []any{project, session}},
			{"inbox_compat_reply", `EXPLAIN SELECT id FROM inbox_compat_messages WHERE project_id=$1 AND reply_to_id=$2`, []any{project, session}},
		}
		for _, check := range checks {
			rows, err := tx.Query(ctx, check.query, check.args...)
			if err != nil {
				return err
			}
			plan := ""
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					rows.Close()
					return err
				}
				plan += line + "\n"
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if !strings.Contains(plan, "Index Scan using "+check.name) && !strings.Contains(plan, "Bitmap Index Scan on "+check.name) {
				return fmt.Errorf("%s lookup does not use its index:\n%s", check.name, plan)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureTenant(t *testing.T) {
	ctx := context.Background()
	if err := db.EnsureTenant(ctx, nil, "", "Name"); err == nil {
		t.Fatal("expected empty slug to fail")
	}
	pool := dbtest.Open(t).Admin
	if err := db.EnsureTenant(ctx, pool, "p02-ensure", "First"); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureTenant(ctx, pool, "p02-ensure", "Second"); err != nil {
		t.Fatal(err)
	}
	var name string
	var n int
	if err := pool.QueryRow(ctx, `SELECT name, count(*) OVER () FROM tenants WHERE slug = 'p02-ensure'`).Scan(&name, &n); err != nil {
		t.Fatal(err)
	}
	if name != "First" || n != 1 {
		t.Fatalf("name=%s n=%d", name, n)
	}
}

func TestRLSIsolatesPrincipals(t *testing.T) {
	ctx := context.Background()
	fresh := dbtest.Open(t)
	admin := fresh.Admin
	rls := fresh.App

	var tenantA, tenantB string
	if err := admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('p02-rls-a', 'A') RETURNING id::text`).Scan(&tenantA); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('p02-rls-b', 'B') RETURNING id::text`).Scan(&tenantB); err != nil {
		t.Fatal(err)
	}

	var idA, idB string
	if err := db.InTenant(ctx, rls, tenantA, func(tx pgx.Tx) error {
		var setting string
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id', true)`).Scan(&setting); err != nil {
			return err
		}
		if setting != tenantA {
			return fmt.Errorf("tenant setting %q", setting)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO principals (tenant_id, kind, name, roles)
			VALUES ($1::uuid, 'person', 'p02-rls-a', $2) RETURNING id::text`,
			tenantA, []string{"member"}).Scan(&idA)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, rls, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO principals (tenant_id, kind, name)
			VALUES ($1::uuid, 'agent', 'p02-rls-b') RETURNING id::text`,
			tenantB).Scan(&idB)
	}); err != nil {
		t.Fatal(err)
	}

	var visibleA, hiddenB int
	if err := db.InTenant(ctx, rls, tenantA, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id = $1::uuid`, idA).Scan(&visibleA); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id = $1::uuid`, idB).Scan(&hiddenB)
	}); err != nil {
		t.Fatal(err)
	}
	if visibleA != 1 || hiddenB != 0 {
		t.Fatalf("tenant A sees own=%d other=%d", visibleA, hiddenB)
	}

	var visibleB int
	if err := db.InTenant(ctx, rls, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id = $1::uuid`, idA).Scan(&visibleB)
	}); err != nil {
		t.Fatal(err)
	}
	if visibleB != 0 {
		t.Fatalf("tenant B sees tenant A principal")
	}

	err := db.InTenant(ctx, rls, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, 'person', 'sneak')`, tenantB)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("expected RLS denial, got %v", err)
	}

	var leaked int
	if err := rls.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id = $1::uuid`, idA).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("principal visible without a tenant setting; set_config was not transaction-local")
	}

	var adminSees int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id = ANY($1::uuid[])`, []string{idA, idB}).Scan(&adminSees); err != nil {
		t.Fatal(err)
	}
	if adminSees != 2 {
		t.Fatalf("superuser sees %d principals, want 2", adminSees)
	}
}
