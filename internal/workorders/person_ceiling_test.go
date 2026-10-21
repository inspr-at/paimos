// SPDX-License-Identifier: AGPL-3.0-only

package workorders

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: work-order preparation keeps the agent's own projects after the run
// or key person is narrower.
func TestPersonCeilingRefresh(t *testing.T) {
	d, agent, projectM, projectL := seedPersonCeiling(t)
	ctx := tenant.WithPrincipal(t.Context(), agent)
	err := db.InTenant(ctx, d.App, agent.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
			return err
		}
		if err := RefreshPrincipal(ctx, tx, agent); err != nil {
			return err
		}
		var vis string
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&vis); err != nil {
			return err
		}
		if vis == "*" || !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) {
			t.Fatalf("refresh visibility %s", vis)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedPersonCeiling(t *testing.T) (*dbtest.DB, tenant.Principal, string, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, person, agentID, projectM, projectL string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling-wo','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		kind string
		dest *string
	}{{"person", &owner}, {"person", &person}, {"agent", &agentID}} {
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'Ceiling') RETURNING id::text`, tid, row.kind).Scan(row.dest); err != nil {
			t.Fatal(err)
		}
	}
	dbtest.BindRole(t, d, tid, owner, "owner")
	dbtest.BindRole(t, d, tid, agentID, "member")
	err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		node := func(title string) (string, error) {
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid, title).Scan(&id)
			return id, err
		}
		var err error
		projectM, err = node("M")
		if err != nil {
			return err
		}
		projectL, err = node("L")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, person, projectM); err != nil {
		t.Fatal(err)
	}
	agent := tenant.Principal{ID: agentID, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, KeyPersonID: person, FullAccess: true}
	return d, agent, projectM, projectL
}
