// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: another agent borrows the lead owner's harness.control through the lead row.
func TestPersonCeilingLeadRefusesAnotherAgent(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, project, leadAgent, otherAgent, session string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('lead-ceiling','Lead ceiling') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, row := range []struct {
			kind, name string
			id         *string
		}{{"person", "Owner", &owner}, {"agent", "Lead", &leadAgent}, {"agent", "Other", &otherAgent}} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, row.kind, row.name).Scan(row.id); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'M' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','fixture','unmanaged','worker',$4,$4) RETURNING id::text`, tid, project, leadAgent, []byte("lead-ceiling")).Scan(&session); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, tid, project, owner, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, owner, "owner")
	dbtest.BindRole(t, d, tid, leadAgent, "member")
	dbtest.BindRole(t, d, tid, otherAgent, "member")
	lead := tenant.Principal{ID: leadAgent, TenantID: tid, Kind: tenant.Agent}
	other := tenant.Principal{ID: otherAgent, TenantID: tid, Kind: tenant.Agent}
	check := func(p tenant.Principal, verified ...string) error {
		t.Helper()
		var err error
		if e := db.InTenant(tenant.WithPrincipal(ctx, p), d.App, tid, func(tx pgx.Tx) error {
			err = leadOwner(ctx, tx, p, project, owner, verified...)
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		return err
	}
	if err := check(other); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("other agent: %v", err)
	}
	if err := check(lead, otherAgent); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("verified mismatch: %v", err)
	}
	if err := check(lead); err != nil {
		t.Fatalf("lead agent: %v", err)
	}
	if err := check(tenant.Principal{ID: owner, TenantID: tid, Kind: tenant.Person}); err != nil {
		t.Fatalf("owner person: %v", err)
	}
}

// Risk: a worker claim or another project's turn is refused because the caller
// is not the lead session's agent. That identity belongs to lead mutations.
func TestPersonCeilingLeadProjectionKeepsOtherCaller(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, project, leadAgent, worker string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('lead-projection','Lead projection') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, row := range []struct {
			kind, name string
			id         *string
		}{{"person", "Owner", &owner}, {"agent", "Lead", &leadAgent}, {"agent", "Worker", &worker}} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, row.kind, row.name).Scan(row.id); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'M' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		var session string
		if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','fixture','unmanaged','coordinator',$4,$4) RETURNING id::text`, tid, project, leadAgent, []byte("lead-projection")).Scan(&session); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, tid, project, owner, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, owner, "owner")
	dbtest.BindRole(t, d, tid, leadAgent, "member")
	dbtest.BindRole(t, d, tid, worker, "member")
	caller := tenant.Principal{ID: worker, TenantID: tid, Kind: tenant.Agent}
	var view Lead
	if err := db.InTenant(tenant.WithPrincipal(ctx, caller), d.App, tid, func(tx pgx.Tx) error {
		l, err := loadLead(ctx, tx, project, false)
		if err != nil {
			return err
		}
		view, err = projectLead(ctx, tx, caller, l)
		return err
	}); err != nil {
		t.Fatalf("worker projection: %v", err)
	}
	if view.State == "cannot_start" && view.Reason == "owner_revoked" {
		t.Fatalf("valid owner projected as revoked: %+v", view)
	}
	if view.State != "working" && view.State != "starting" && view.State != "waiting_for_room" && view.State != "paused" {
		t.Fatalf("worker projection lost the lead: %+v", view)
	}
}
