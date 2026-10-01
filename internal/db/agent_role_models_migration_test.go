// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentRoleModelsBackfillAcrossTenantsUnderRLS(t *testing.T) {
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
		if name != "1061_agent_roles_models_read.sql" {
			return nil
		}
		for i := 0; i < 2; i++ {
			var tid string
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Models migration') RETURNING id::text`, fmt.Sprintf("models-backfill-%d", i)).Scan(&tid); err != nil {
				return err
			}
			tenants = append(tenants, tid)
			if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
				var agent, person, role, personRole string
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Existing agent') RETURNING id::text`, tid).Scan(&agent); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Existing person') RETURNING id::text`, tid).Scan(&person); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'existing_agent','Existing agent') RETURNING id::text`, tid).Scan(&role); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'person_only','Person only') RETURNING id::text`, tid).Scan(&personRole); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$3,'nodes.read')`, tid, role, personRole); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace'),($1,$4,$5,'workspace')`, tid, agent, role, person, personRole); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1::uuid,$2::uuid,'Existing key',($1::uuid)::text,'test-only',ARRAY['nodes.read'])`, tid, agent)
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
	for _, tid := range tenants {
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			var correct bool
			err := tx.QueryRow(ctx, `SELECT
    (SELECT count(*)=2 FROM role_permissions rp JOIN roles r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id WHERE r.key='existing_agent' AND rp.permission IN ('nodes.read','models.read'))
    AND NOT EXISTS(SELECT 1 FROM role_permissions rp JOIN roles r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id WHERE r.key='person_only' AND rp.permission='models.read')
    AND (SELECT scopes=ARRAY['nodes.read'] FROM agent_keys WHERE name='Existing key')`).Scan(&correct)
			if err == nil && !correct {
				return fmt.Errorf("agent role was not additively backfilled under tenant RLS")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Repeat only this additive migration through a single-connection pool.
	// The migration runner already owns the connection carrying the lock.
	if _, err := d.App.Exec(ctx, `DELETE FROM schema_migrations WHERE version='1061_agent_roles_models_read.sql'`); err != nil {
		t.Fatal(err)
	}
	cfg := d.App.Config()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	if err := db.MigrateWithHook(ctx, single, nil); err != nil {
		t.Fatalf("idempotent single-connection migration: %v", err)
	}
}
