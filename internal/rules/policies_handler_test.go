// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Handler-level pin: no middleware, no body/DB work before the person check.
func TestPoliciesRulesPublishHandlerPersonRequired(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	p := tenant.Principal{ID: id, TenantID: id, Kind: tenant.Agent}
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	r := httptest.NewRequest("POST", "/api/rules/sets/"+id+"/publish", nil).WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var out Error
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 || out.Code != "forbidden" || out.Message != "permission or scoped ownership denied" {
		t.Fatalf("wrong handler refusal %d %s", w.Code, w.Body.String())
	}
}

// A custom role cannot make the person/workstation publish permission
// grantable to an ordinary agent, even with an injected key scope.
func TestPoliciesRulesPublishTransactionHelperRejectsOrdinaryAgentScope(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{Kind: tenant.Agent, Scopes: []string{"rules.publish"}}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('policies-rule-pin','Policies') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Injected helper caller') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		var rid string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'helper_pin','Helper pin') RETURNING id::text`, p.TenantID).Scan(&rid); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'rules.publish')`, p.TenantID, rid); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, p.ID, rid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), p)
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		var granted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_bindings b JOIN role_permissions r ON r.tenant_id=b.tenant_id AND r.role_id=b.role_id WHERE b.principal_id=$1 AND r.permission='rules.publish')`, p.ID).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			t.Fatal("fixture lost its explicit role grant")
		}
		if err := authz.RequireTx(ctx, tx, p, "rules.publish", authz.Scope{}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("ordinary agent gained publish authority: %v", err)
		}
		err := permission(ctx, tx, p, Scope{Layer: "company"}, "rules.publish")
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("person helper allowed agent: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
