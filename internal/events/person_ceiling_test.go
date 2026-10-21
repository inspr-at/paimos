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
	var outcomeM, outcomeL string
	err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, slug := range []string{"goal", "outcome"} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$2,upper(left($2,4)),'goal') ON CONFLICT DO NOTHING`, tid, slug); err != nil {
				return err
			}
		}
		node := func(slug, title, parent string) (string, error) {
			var id string
			var parentID any
			if parent != "" {
				parentID = parent
			}
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,$4 FROM node_kinds WHERE tenant_id=$1 AND slug=$3 RETURNING id::text`, tid, title, slug, parentID).Scan(&id)
			return id, err
		}
		var err error
		if projectM, err = node("project", "M", ""); err != nil {
			return err
		}
		if projectL, err = node("project", "L", ""); err != nil {
			return err
		}
		goalM, err := node("goal", "Goal M", projectM)
		if err != nil {
			return err
		}
		if outcomeM, err = node("outcome", "Outcome M", goalM); err != nil {
			return err
		}
		goalL, err := node("goal", "Goal L", projectL)
		if err != nil {
			return err
		}
		outcomeL, err = node("outcome", "Outcome L", goalL)
		return err
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
		first, err := Append(ctx, tx, actor, Change{NodeID: &outcomeM, Type: "goal.created", After: map[string]any{"n": 1}})
		if err != nil {
			return err
		}
		second, err := Append(ctx, tx, actor, Change{NodeID: &outcomeL, Type: "goal.created", After: map[string]any{"n": 2}})
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
