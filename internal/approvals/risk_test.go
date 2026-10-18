// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestApprovalRiskProjection(t *testing.T) {
	f := newFixture(t)
	// Expand only this fixture key; requests still pass the real scope ceiling.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['nodes','harness','release','run','journey','approvals.read'] WHERE principal_id=$1::uuid AND scopes=ARRAY['run','nodes.read','approvals.read','harness.read']::text[]`, f.agentA.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ scope, kind, risk string }{{"nodes.read", "node", "low"}, {"nodes.read.fields", "node", "low"}, {"nodes.read", "tenant", "high"}, {"harness.control", "node", "high"}, {"release.deploy", "node", "high"}, {"journey.build", "node", "medium"}, {"journey.deploy", "node", "high"}, {"nodes.delete", "node", "high"}, {"nodes.delete.read", "node", "high"}, {"nodes.write", "node", "medium"}, {"run.claim", "run", "medium"}} {
		t.Run(tc.scope+"-"+tc.kind, func(t *testing.T) {
			resource := &f.nodeA
			if tc.kind == "tenant" {
				resource = nil
			} else if tc.kind == "run" {
				resource = &f.runA
			}
			response := f.do(f.agentA, f.wide, "POST", "/api/approvals", proposalJSON(tc.scope, tc.kind, resource, nil))
			if response.Code != 201 {
				t.Fatalf("propose %d %s", response.Code, response.Body.String())
			}
			a := decodeApproval(t, response)
			if a.Risk != tc.risk {
				t.Fatalf("risk %s want %s", a.Risk, tc.risk)
			}
			response = f.do(f.personA, "", "POST", "/api/approvals/"+a.ID+"/decision", `{"decision":"approved"}`)
			if response.Code != 200 || decodeApproval(t, response).Risk != tc.risk {
				t.Fatal("decision risk missing")
			}
		})
	}
	if response := f.do(f.agentA, f.wide, "POST", "/api/approvals", proposalJSON("nodes.readiness", "node", &f.nodeA, nil)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission was proposed: %d %s", response.Code, response.Body.String())
	}
	response := f.do(f.personA, "", "GET", "/api/approvals", "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var items []Approval
	if err = json.Unmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 11 {
		t.Fatal("missing approvals")
	}
	for _, item := range items {
		if item.Risk == "" {
			t.Fatal("list risk missing")
		}
	}
	for _, p := range []struct {
		foreign bool
		want    int
	}{{true, 0}, {false, 0}} {
		principal := f.personB
		if !p.foreign {
			principal = f.agentB
		}
		response = f.do(principal, "", "GET", "/api/approvals", "")
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if p.want == 0 && strings.TrimSpace(response.Body.String()) != "[]" {
			t.Fatalf("empty approvals encoded as %s", response.Body.String())
		}
		if err = json.Unmarshal(response.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != p.want {
			t.Fatal("risk list leaked approvals")
		}
	}
	if response = f.anon("GET", "/api/approvals", ""); response.Code != 401 {
		t.Fatal("anonymous approvals")
	}
	if response = f.do(f.agentA, f.empty, "POST", "/api/approvals", proposalJSON("nodes.delete", "node", &f.nodeA, nil)); response.Code != 403 {
		t.Fatal("risk bypassed scope ceiling")
	}
}

func TestDecisionRequiresPermissionBeingGranted(t *testing.T) {
	f := newFixture(t)
	decider := insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, "custom decider")
	var roleID string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'decision_only','Decision only') RETURNING id::text`, f.tenantA).Scan(&roleID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'approvals.decide')`, f.tenantA, roleID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, f.tenantA, decider.ID, roleID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed := f.do(f.agentA, f.narrow, http.MethodPost, "/api/approvals", proposalJSON("nodes.read", "node", &f.nodeA, nil))
	if proposed.Code != http.StatusCreated {
		t.Fatal(proposed.Body.String())
	}
	id := decodeApproval(t, proposed).ID
	path := "/api/approvals/" + id + "/decision"
	if got := f.do(decider, "", http.MethodPost, path, `{"decision":"approved"}`); got.Code != http.StatusForbidden {
		t.Fatalf("decision without nodes.read: %d %s", got.Code, got.Body.String())
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'nodes.read')`, f.tenantA, roleID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.do(decider, "", http.MethodPost, path, `{"decision":"approved"}`); got.Code != http.StatusOK {
		t.Fatalf("decision with nodes.read: %d %s", got.Code, got.Body.String())
	}
}

func TestDecisionRiskRequiresPersonRole(t *testing.T) {
	f := newFixture(t)
	person := func(name, role string) tenant.Principal {
		p := tenant.Principal{TenantID: f.tenantA, Kind: tenant.Person, Name: name, Roles: []string{role}}
		err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,$3) RETURNING id::text`, f.tenantA, name, p.Roles).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, f.db, f.tenantA, p.ID)
		return p
	}
	member := person("member", "member")
	customer := person("customer", "customer")
	super := person("global-admin", "super_admin")
	for _, tc := range []struct {
		scope, kind string
		resource    *string
		risk        string
		decider     tenant.Principal
		want        int
	}{
		{"nodes.read", "node", &f.nodeA, "low", member, http.StatusOK},
		{"run.claim", "run", &f.runA, "medium", member, http.StatusOK},
		{"nodes.read", "tenant", nil, "high", member, http.StatusForbidden},
		{"nodes.read", "node", &f.nodeA, "low", customer, http.StatusForbidden},
		{"nodes.read", "tenant", nil, "high", super, http.StatusOK},
	} {
		proposal := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", proposalJSON(tc.scope, tc.kind, tc.resource, nil))
		if proposal.Code != http.StatusCreated {
			t.Fatalf("proposal %s: %d %s", tc.risk, proposal.Code, proposal.Body.String())
		}
		a := decodeApproval(t, proposal)
		if a.Risk != tc.risk {
			t.Fatalf("risk %s: %s", tc.risk, a.Risk)
		}
		decision := f.do(tc.decider, "", http.MethodPost, "/api/approvals/"+a.ID+"/decision", `{"decision":"approved"}`)
		if decision.Code != tc.want {
			t.Errorf("%s by %s: %d %s", tc.risk, tc.decider.Name, decision.Code, decision.Body.String())
		}
		if tc.risk == "high" && tc.decider.Name == "member" {
			adminDecision := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+a.ID+"/decision", `{"decision":"approved"}`)
			if adminDecision.Code != http.StatusOK {
				t.Fatalf("admin high: %d %s", adminDecision.Code, adminDecision.Body.String())
			}
		}
	}
}
