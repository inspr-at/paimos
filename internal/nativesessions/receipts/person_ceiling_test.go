// SPDX-License-Identifier: AGPL-3.0-only

package receipts

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a native receipt write keeps the agent's projects, or its harness
// binding resolves a run without installing that run's person.
func TestPersonCeilingReceipts(t *testing.T) {
	d, agent, projectM, projectL, session, run := seedReceiptCeiling(t)
	ctx := tenant.WithPrincipal(t.Context(), agent)
	repo := &Repository{pool: d.App}
	err := repo.write(ctx, agent, func(ctx context.Context, tx pgx.Tx) error {
		next, err := runContext(ctx, tx, agent, nativecontract.Binding{HarnessSessionID: session, RunID: &run})
		if err != nil {
			return err
		}
		got, ok := tenant.PrincipalFrom(next)
		if !ok || got.KeyPersonID != agent.KeyPersonID {
			t.Fatalf("run context principal %+v", got)
		}
		var vis string
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&vis); err != nil {
			return err
		}
		if vis == "*" || !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) {
			t.Fatalf("receipt visibility %s", vis)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedReceiptCeiling(t *testing.T) (*dbtest.DB, tenant.Principal, string, string, string, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, person, agentID, projectM, projectL, session, run string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling-receipts','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
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
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'M' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectM); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'L' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectL); err != nil {
			return err
		}
		var order string
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Order',$2 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='work_order' RETURNING id::text`, tid, projectM).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'ready')`, tid, order, owner); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,acting_person_id) VALUES($1,$2,$3,$4) RETURNING id::text`, tid, order, agentID, person).Scan(&run); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte("aeon.harness.lease\x00receipt-ceiling"))
		ref := sha256.Sum256([]byte("receipt-ceiling-ref"))
		return tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,run_id,harness,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'grok','ceiling-host','unmanaged','worker','unknown',$6,$7) RETURNING id::text`, tid, projectM, agentID, owner, run, ref[:], sum[:]).Scan(&session)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, person, projectM); err != nil {
		t.Fatal(err)
	}
	agent := tenant.Principal{ID: agentID, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, KeyPersonID: person, FullAccess: true}
	return d, agent, projectM, projectL, session, run
}
