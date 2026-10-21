// SPDX-License-Identifier: AGPL-3.0-only

package recurrences

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: recurrence source inspection uses the service caller instead of the
// definition owner. enterOwnerCeiling is the entry sourceContext uses before
// it restores service visibility for the later write.
func TestPersonCeilingRecurrence(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, callerID, projectM, projectL, knowledge string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling-recurrence','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		kind string
		dest *string
	}{{"person", &owner}, {"agent", &callerID}} {
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
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, owner, projectM); err != nil {
		t.Fatal(err)
	}
	caller := tenant.Principal{ID: callerID, TenantID: tid, Kind: tenant.Agent}
	err = db.InTenant(tenant.WithPrincipal(ctx, caller), d.App, tid, func(tx pgx.Tx) error {
		var before string
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&before); err != nil {
			return err
		}
		if before == "*" || contains(before, projectM) {
			t.Fatalf("caller started at %s", before)
		}
		if err := enterOwnerCeiling(ctx, tx, tid, owner); err != nil {
			return err
		}
		var vis string
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&vis); err != nil {
			return err
		}
		if vis == "*" || !contains(vis, projectM) || contains(vis, projectL) {
			t.Fatalf("owner visibility %s", vis)
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
				t.Fatalf("node %s visible=%v under %s", row.id, n == 1, vis)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func contains(vis, id string) bool {
	return len(id) > 0 && len(vis) >= len(id) && (vis == "*" || vis == "{"+id+"}" || (len(vis) > len(id) && (vis[0] == '{' && has(vis, id))))
}

func has(vis, id string) bool {
	for i := 0; i+len(id) <= len(vis); i++ {
		if vis[i:i+len(id)] == id {
			return true
		}
	}
	return false
}
