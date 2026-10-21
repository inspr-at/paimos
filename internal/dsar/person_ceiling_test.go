// SPDX-License-Identifier: AGPL-3.0-only

package dsar

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a subject export keeps the operator's projects. The export enters as
// the subject person with a null key creator and a null acting person.
func TestPersonCeilingDSAR(t *testing.T) {
	d, person, operator, projectM, projectL, knowledge := seedSubjectCeiling(t, "person-ceiling-dsar")
	ctx := tenant.WithPrincipal(t.Context(), operator)
	if _, err := Collect(ctx, d.App, Options{Tenant: "person-ceiling-dsar", ActorID: person, Person: person}); err == nil || !strings.Contains(err.Error(), "actor must be the calling person") {
		t.Fatalf("agent caller: %v", err)
	}
	err := db.InTenant(ctx, d.App, operator.TenantID, func(tx pgx.Tx) error {
		if err := enterSubjectVisibility(ctx, tx, operator.TenantID, person); err != nil {
			return err
		}
		var vis string
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&vis); err != nil {
			return err
		}
		if vis == "*" || !visibilityHas(vis, projectM) || visibilityHas(vis, projectL) {
			t.Fatalf("subject visibility %s", vis)
		}
		for _, row := range []struct {
			id   string
			want bool
		}{{projectM, true}, {projectL, false}, {knowledge, false}} {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1::uuid`, row.id).Scan(&n); err != nil {
				return err
			}
			if (n == 1) != row.want {
				t.Fatalf("node %s visible=%v", row.id, n == 1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func visibilityHas(vis, id string) bool {
	if vis == "*" {
		return true
	}
	for i := 0; i+len(id) <= len(vis); i++ {
		if vis[i:i+len(id)] == id {
			return true
		}
	}
	return false
}

func seedSubjectCeiling(t *testing.T, slug string) (*dbtest.DB, string, tenant.Principal, string, string, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, person, operatorID, projectM, projectL, knowledge string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Person ceiling') RETURNING id::text`, slug).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		kind string
		dest *string
	}{{"person", &person}, {"agent", &operatorID}} {
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'Ceiling') RETURNING id::text`, tid, row.kind).Scan(row.dest); err != nil {
			t.Fatal(err)
		}
	}
	err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		node := func(slug, title string) (string, error) {
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2 FROM node_kinds WHERE tenant_id=$1 AND slug=$3 RETURNING id::text`, tid, title, slug).Scan(&id)
			return id, err
		}
		var err error
		if projectM, err = node("project", "M"); err != nil {
			return err
		}
		if projectL, err = node("project", "L"); err != nil {
			return err
		}
		knowledge, err = node("memory", "workspace knowledge")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, person, projectM); err != nil {
		t.Fatal(err)
	}
	operator := tenant.Principal{ID: operatorID, TenantID: tid, Kind: tenant.Agent}
	return d, person, operator, projectM, projectL, knowledge
}
