// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: forced RLS must not silently skip either tracker tenant, or mark a
// different scoped/service/revoked key because it resembles a full preset.
func TestFullAccessMigrationAcrossTenants(t *testing.T) {
	ctx := dbtest.Seed(t.Context())
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenants []string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1292_agent_key_full_access.sql" {
			return nil
		}
		for _, slug := range []string{"ppm-shaped", "pma-shaped"} {
			var id string
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Full-access migration') RETURNING id::text`, slug).Scan(&id); err != nil {
				return err
			}
			tenants = append(tenants, id)
			if err := db.InTenant(ctx, d.App, id, func(tx pgx.Tx) error {
				for i, fixture := range []struct {
					name, status, kind        string
					service, revoked, expired bool
				}{
					{"workstation-agents", "active", "agent", false, false, false},
					{"workstation-agents", "active", "agent", false, false, true},
					{"different-full-preset", "active", "agent", false, false, false},
					{"workstation-agents", "active", "agent", false, true, false},
					{"workstation-agents", "deactivated", "agent", false, false, false},
					{"workstation-agents", "active", "agent", true, false, false},
					{"Workstation-agents", "active", "agent", false, false, false},
					{"workstation-agents", "active", "person", false, false, false},
				} {
					var principal string
					if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,status,roles)
 VALUES($1,$2,$3,$4,CASE WHEN $5 THEN ARRAY['system'] ELSE '{}'::text[] END) RETURNING id::text`, id, fixture.kind, fmt.Sprintf("fixture-%d", i), fixture.status, fixture.service).Scan(&principal); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,expires_at,revoked_at)
 VALUES($1,$2,$3,$4,'test-only',ARRAY['nodes.read','events.read'],CASE WHEN $5 THEN now()-interval '1 day' END,CASE WHEN $6 THEN now() END)`, id, principal, fixture.name, fmt.Sprintf("%s-%d", id, i), fixture.expired, fixture.revoked); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range tenants {
		if err := db.InTenant(ctx, d.App, id, func(tx pgx.Tx) error {
			var good bool
			err := tx.QueryRow(ctx, `SELECT count(*)=8 AND count(*) FILTER(WHERE full_access)=2
 AND bool_and(coalesce(full_access,false)=(k.name='workstation-agents' AND k.revoked_at IS NULL AND p.kind='agent' AND p.status='active' AND p.roles='{}'::text[]))
 AND bool_and(k.scopes=ARRAY['nodes.read','events.read']) AND bool_and(NOT k.owner_workstation)
 FROM agent_keys k JOIN principals p ON p.tenant_id=k.tenant_id AND p.id=k.principal_id`).Scan(&good)
			if err != nil {
				return err
			}
			if !good {
				return fmt.Errorf("migration selection, rollback scopes or workstation designation changed in %s", id)
			}
			// Old writers omit the nullable column and remain in scoped mode.
			_, err = tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes)
 SELECT tenant_id,principal_id,'older-writer',tenant_id::text||'-older','test-only','{}'::text[] FROM agent_keys LIMIT 1`)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT full_access=false FROM agent_keys WHERE name='older-writer'`).Scan(&good); err != nil {
				return err
			}
			if !good {
				return fmt.Errorf("older writer gained full access")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(ctx, d.App, nil); err != nil {
		t.Fatalf("repeated migration: %v", err)
	}
}
