// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestKnownRouteArea(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "ticket-route", "person", "Lead", []string{"admin"})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var project, otherProject string
		for i, dst := range []*string{&project, &otherProject} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'ROUTE-'||$2::text,'Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID, i+1).Scan(dst); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_kinds(tenant_id,slug,label,project_id) VALUES($1,'firmware','Firmware',$2),($1,'archived','Archived',NULL)`, p.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_kinds SET archived_at=now() WHERE slug='archived'`); err != nil {
			return err
		}
		for _, tc := range []struct {
			area, project string
			want          bool
		}{
			{"backend", "", true}, {" frontend ", "", true}, {"security", project, true},
			{"firmware", project, true}, {"firmware", otherProject, false}, {"firmware", "", false},
			{"archived", "", false}, {"", "", false}, {"unknown", project, false}, {"review", project, false}, {"other", project, false},
		} {
			known, err := KnownRouteArea(t.Context(), tx, tc.area, tc.project)
			if err != nil {
				return err
			}
			if known != tc.want {
				t.Fatalf("%q in %q: %t, want %t", tc.area, tc.project, known, tc.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
