// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"testing"
)

// Risk: subscription's widened service query bypasses outcome/serves RLS.
func TestGoalsSubscriptionUsesOutcomeVisibility(t *testing.T) {
	d, p, _ := fixture(t)
	ctx := t.Context()
	var owner, role, project, hidden, goal, outcome, hiddenOutcome string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, p.TenantID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, owner, "owner")
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'subscribe_goals','Subscribe') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'events.subscribe')`, p.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, p.ID, role); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
		for _, slug := range []string{"goal", "outcome"} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$2,upper($2),'goal')`, p.TenantID, slug); err != nil {
				return err
			}
		}
		node := func(slug, parent string) (string, error) {
			var id string
			var dest any
			if parent != "" {
				dest = parent
			}
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Fixture',$3 FROM node_kinds WHERE tenant_id=$1 AND slug=$2 RETURNING id::text`, p.TenantID, slug, dest).Scan(&id)
			return id, err
		}
		var err error
		project, err = node("project", "")
		if err != nil {
			return err
		}
		hidden, err = node("project", "")
		if err != nil {
			return err
		}
		goal, err = node("goal", project)
		if err != nil {
			return err
		}
		outcome, err = node("outcome", goal)
		if err != nil {
			return err
		}
		hiddenOutcome, err = node("outcome", goal)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, p.TenantID, hiddenOutcome, hidden); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, p.TenantID, p.ID, project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p.KeyCreatorID = owner
	p.Scopes = []string{"events.subscribe", "nodes.read"}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','goals',repeat('0',64),$3,$4) RETURNING id::text`, p.TenantID, p.ID, p.Scopes, owner).Scan(&p.KeyID); err != nil {
		t.Fatal(err)
	}
	actor := tenant.Principal{ID: owner, TenantID: p.TenantID, Kind: tenant.Person}
	var visibleID, through int64
	err = db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
		for i, id := range []string{outcome, hiddenOutcome, goal} {
			event, err := Append(ctx, tx, actor, Change{NodeID: &id, Type: "goal.eta_changed", After: map[string]any{"goal_id": goal, "read_through": 0, "generation": 1}})
			if err != nil {
				return err
			}
			if i == 0 {
				visibleID = event.ID
			}
			through = event.ID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hints, _, err := New(d.App).(*module).subscriptionRead(tenant.WithPrincipal(ctx, p), p, 0, through, map[string]bool{"goals": true})
	if err != nil || len(hints) != 2 || hints[0].ID != visibleID || hints[1].ID != through {
		t.Fatalf("goal hints %+v %v", hints, err)
	}
}

// Risk (AEON-1494): the workspace status note's node-less event never reaches
// a topics=goals subscriber, or reaches a key that only sees one project.
func TestGoalsSubscriptionDeliversStatusNoteToWorkspaceReaders(t *testing.T) {
	d, p, _ := fixture(t)
	ctx := t.Context()
	var role, author, project string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'subscribe_note','Subscribe') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'events.subscribe')`, p.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Author') RETURNING id::text`, p.TenantID).Scan(&author); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, author, "owner")
	var readRole string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'subscribe_read','Subscribe and read') RETURNING id::text`, p.TenantID).Scan(&readRole); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'events.subscribe')`, p.TenantID, readRole); err != nil {
		t.Fatal(err)
	}
	readers := map[string]tenant.Principal{}
	for name, bound := range map[string]string{"workspace": readRole, "project": role} {
		key := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Agent, KeyCreatorID: author, Scopes: []string{"events.subscribe", "nodes.read"}}
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent',$2) RETURNING id::text`, p.TenantID, name).Scan(&key.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, key.ID, bound); err != nil {
			t.Fatal(err)
		}
		if err := d.Admin.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture',$3,repeat('0',64),$4,$5) RETURNING id::text`, p.TenantID, key.ID, name, key.Scopes, author).Scan(&key.KeyID); err != nil {
			t.Fatal(err)
		}
		readers[name] = key
	}
	// One workspace role per principal: the workspace key's role also reads nodes.
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,'nodes.read' FROM roles WHERE tenant_id=$1 AND key='subscribe_read'`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	var through int64
	err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, p.TenantID, readers["project"].ID, project); err != nil {
			return err
		}
		event, err := Append(ctx, tx, tenant.Principal{ID: author, TenantID: p.TenantID, Kind: tenant.Person}, Change{Type: "goal.status_note_changed", After: map[string]any{"text": "Status", "by": author}})
		through = event.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(d.App).(*module)
	for name, want := range map[string]int{"workspace": 1, "project": 0} {
		reader := readers[name]
		hints, _, err := m.subscriptionRead(tenant.WithPrincipal(ctx, reader), reader, 0, through, map[string]bool{"goals": true})
		if err != nil || len(hints) != want || want == 1 && (hints[0].ID != through || hints[0].Type != "goal.status_note_changed" || hints[0].Topic != "goals") {
			t.Fatalf("%s reader hints %+v %v", name, hints, err)
		}
	}
}
