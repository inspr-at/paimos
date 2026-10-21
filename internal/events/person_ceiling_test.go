// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a goal subscription hint names a project the acting person cannot read.
// The workspace role grants only events.subscribe, so SQL visibility is the
// project intersection rather than every project.
func TestPersonCeilingEvents(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, person, agentID, role, projectM, projectL string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling-events','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
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
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'ceiling_subscribe','Ceiling subscribe') RETURNING id::text`, tid).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'events.subscribe')`, tid, role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace'),($1,$4,$3,'workspace')`, tid, agentID, role, person); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'M' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectM); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'L' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectL)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, person, projectM); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, agentID, projectM); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, agentID, projectL); err != nil {
		t.Fatal(err)
	}
	agent := tenant.Principal{ID: agentID, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, KeyPersonID: person, Scopes: []string{"events.subscribe", "nodes.read"}}
	var onM, onL int64
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		actor := tenant.Principal{ID: owner, TenantID: tid, Kind: tenant.Person}
		first, err := Append(ctx, tx, actor, Change{NodeID: &projectM, Type: "goal.created", After: map[string]any{"n": 1}})
		if err != nil {
			return err
		}
		second, err := Append(ctx, tx, actor, Change{NodeID: &projectL, Type: "goal.created", After: map[string]any{"n": 2}})
		onM, onL = first.ID, second.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	hints, _, err := New(d.App).(*module).subscriptionRead(tenant.WithPrincipal(ctx, agent), agent, 0, onL, map[string]bool{"goals": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || hints[0].ID != onM || hints[0].Type != "goal.created" || hints[0].Topic != "goals" {
		t.Fatalf("goal hints %+v", hints)
	}
}
