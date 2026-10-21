// SPDX-License-Identifier: AGPL-3.0-only

package rules

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

// Risk: rules preparation or the rules HTTP write keeps projects the acting
// person cannot see. The saved rules project list is the ceiling, because
// enterRules then opens workspace visibility for company rule nodes.
func TestPersonCeilingRules(t *testing.T) {
	d, agent, projectM, projectL := seedRuleCeiling(t)
	ctx := tenant.WithPrincipal(t.Context(), agent)
	err := db.InTenant(ctx, d.App, agent.TenantID, func(tx pgx.Tx) error {
		if err := PrepareWrite(ctx, tx, agent); err != nil {
			return err
		}
		return assertRulesCeiling(t, ctx, tx, projectM, projectL)
	})
	if err != nil {
		t.Fatal(err)
	}
	var saved string
	h := (&Module{pool: d.App, versionNow: time.Now}).endpoint("nodes.read", "", func(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
		err := tx.QueryRow(r.Context(), `SELECT coalesce(current_setting('aeon.rules_projects',true),'')`).Scan(&saved)
		return map[string]string{"projects": saved}, err
	})
	req := httptest.NewRequest(http.MethodPost, "/api/rules/ceiling-probe", strings.NewReader(""))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("rules endpoint %d %s", rec.Code, rec.Body.String())
	}
	if saved == "*" || !strings.Contains(saved, projectM) || strings.Contains(saved, projectL) {
		t.Fatalf("endpoint rules projects %s", saved)
	}
}

func assertRulesCeiling(t *testing.T, ctx context.Context, tx pgx.Tx, projectM, projectL string) error {
	t.Helper()
	var saved string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.rules_projects',true),'')`).Scan(&saved); err != nil {
		return err
	}
	if saved == "*" || !strings.Contains(saved, projectM) || strings.Contains(saved, projectL) {
		t.Fatalf("rules projects %s", saved)
	}
	return nil
}

func seedRuleCeiling(t *testing.T) (*dbtest.DB, tenant.Principal, string, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, person, agentID, projectM, projectL string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling-rules','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
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
