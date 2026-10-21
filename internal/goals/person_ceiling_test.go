// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a goal read or write keeps projects the acting person cannot see.
func TestPersonCeilingGoals(t *testing.T) {
	d, agent, projectM, projectL := seedGoalCeiling(t)
	ctx := tenant.WithPrincipal(t.Context(), agent)
	err := db.InTenant(ctx, d.App, agent.TenantID, func(tx pgx.Tx) error {
		if err := lockWrite(ctx, tx, agent); err != nil {
			return err
		}
		return assertCeiling(t, ctx, tx, projectM, projectL)
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/goals", nil)
	req = req.WithContext(ctx)
	m := &module{pool: d.App, now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }}
	err = m.readSnapshot(req, agent, func(ctx context.Context, tx pgx.Tx, _ Watermark, _ time.Time) error {
		return assertCeiling(t, ctx, tx, projectM, projectL)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertCeiling(t *testing.T, ctx context.Context, tx pgx.Tx, projectM, projectL string) error {
	t.Helper()
	var vis string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&vis); err != nil {
		return err
	}
	if vis == "*" || !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) {
		t.Fatalf("goal visibility %s", vis)
	}
	return nil
}

func seedGoalCeiling(t *testing.T) (*dbtest.DB, tenant.Principal, string, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, person, agentID, projectM, projectL string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling-goals','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
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
		var err error
		if err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'M' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectM); err != nil {
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
	return d, tenant.Principal{ID: agentID, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, KeyPersonID: person, FullAccess: true}, projectM, projectL
}
