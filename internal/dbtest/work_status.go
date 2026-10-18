// SPDX-License-Identifier: AGPL-3.0-only
package dbtest

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// EnableWorkParentStatus installs the AEON-429 storage contract when that
// separately authored dependency is not stacked into a worker's fixture.
// It never substitutes a production feature service or bypasses FORCE RLS.
func EnableWorkParentStatus(t testing.TB, d *DB, tenantID string) {
	t.Helper()
	if _, err := d.App.Exec(t.Context(), `DO $$ BEGIN IF to_regclass('public.features') IS NULL THEN
 CREATE TABLE features(tenant_id uuid NOT NULL REFERENCES tenants(id),key text NOT NULL,project_id uuid,
 enabled boolean DEFAULT false,revision bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE NULLS NOT DISTINCT(tenant_id,key,project_id),FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id));
 ALTER TABLE features ENABLE ROW LEVEL SECURITY; ALTER TABLE features FORCE ROW LEVEL SECURITY;
 CREATE POLICY work_fixture_tenant ON features USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
 CREATE POLICY work_fixture_project ON features AS RESTRICTIVE USING(project_id IS NULL OR aeon_visible_all() OR project_id=ANY(aeon_visible_projects()));
 END IF; END $$`); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT aeon_authz_system_actor($1)`, tenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO features(tenant_id,key,enabled) VALUES($1,'work-parent-status',true) ON CONFLICT(tenant_id,key,project_id) DO UPDATE SET enabled=true`, tenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
