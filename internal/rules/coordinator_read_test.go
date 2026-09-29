// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCoordinatorReadsRuleDraftsOnlyOnItsProjects(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('rules-coord','Rules coordinator') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	person := func(name, role string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: name}
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, tid, name).Scan(&p.ID)
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, p.ID, role)
		return p
	}
	admin := person("owner", "admin")
	other := person("other", "admin")
	var projectA, projectB string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RC-1','A' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectA); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RC-2','B' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectB)
	}); err != nil {
		t.Fatal(err)
	}
	coordinator := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "lead", KeyCreatorID: admin.ID, Scopes: append([]string{}, authz.CoordinatorBaseScopes...)}
	worker := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "worker", KeyCreatorID: admin.ID, Scopes: []string{"nodes.read"}}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','lead') RETURNING id::text`, tid).Scan(&coordinator.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','worker') RETURNING id::text`, tid).Scan(&worker.ID); err != nil {
			return err
		}
		var workspace, projectRole string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'coord_ws','Coordinator') RETURNING id::text`, tid).Scan(&workspace); err != nil {
			return err
		}
		for _, permission := range []string{"harness.read", "harness.write", "harness.worker", "inbox.read", "inbox.send", "work_orders.read"} {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, tid, workspace, permission); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, coordinator.ID, workspace); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'coord_a','Project A') RETURNING id::text`, tid).Scan(&projectRole); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, tid, projectRole); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4::uuid)`, tid, coordinator.ID, projectRole, projectA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, worker.ID, "admin")

	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(p tenant.Principal, method, path string, in any) (int, []byte) {
		t.Helper()
		body := ""
		if in != nil {
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			body = string(raw)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}
	must := func(p tenant.Principal, method, path string, in any, want int) []byte {
		t.Helper()
		status, raw := call(p, method, path, in)
		if status != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, status, want, raw)
		}
		return raw
	}
	var company, own, foreign, layerA, layerB Layer
	json.Unmarshal(must(admin, http.MethodPost, "/api/rules/layers", Scope{Layer: "company"}, http.StatusOK), &company)
	json.Unmarshal(must(admin, http.MethodPost, "/api/rules/layers", Scope{Layer: "person", OwnerID: admin.ID}, http.StatusOK), &own)
	json.Unmarshal(must(other, http.MethodPost, "/api/rules/layers", Scope{Layer: "person", OwnerID: other.ID}, http.StatusOK), &foreign)
	json.Unmarshal(must(admin, http.MethodPost, "/api/rules/layers", Scope{Layer: "project", ProjectID: projectA}, http.StatusOK), &layerA)
	json.Unmarshal(must(admin, http.MethodPost, "/api/rules/layers", Scope{Layer: "project", ProjectID: projectB}, http.StatusOK), &layerB)

	status, raw := call(worker, http.MethodGet, "/api/rules/layers", nil)
	if status != http.StatusForbidden {
		t.Fatalf("worker layers: %d %s", status, raw)
	}
	var listed struct {
		Layers []Layer `json:"layers"`
	}
	if err := json.Unmarshal(must(coordinator, http.MethodGet, "/api/rules/layers", nil, http.StatusOK), &listed); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, layer := range listed.Layers {
		seen[layer.ID] = true
	}
	if !seen[company.ID] || !seen[own.ID] || !seen[layerA.ID] || seen[layerB.ID] || seen[foreign.ID] {
		t.Fatalf("coordinator layers = %+v", listed.Layers)
	}
	must(coordinator, http.MethodGet, "/api/rules/sets?layer_id="+layerA.ID, nil, http.StatusOK)
	if status, raw = call(coordinator, http.MethodGet, "/api/rules/sets?layer_id="+layerB.ID, nil); status == http.StatusOK {
		t.Fatalf("coordinator read project B sets: %s", raw)
	}
	must(coordinator, http.MethodPost, "/api/rules/layers", Scope{Layer: "company"}, http.StatusForbidden)
	locked := testRule("safety", "Keep the locked company floor.")
	locked.Strength = "locked"
	var set Set
	json.Unmarshal(must(admin, http.MethodPost, "/api/rules/sets", map[string]any{"layer_id": company.ID, "name": "Safety"}, http.StatusOK), &set)
	must(admin, http.MethodPut, "/api/rules/sets/"+set.ID+"/draft", draftInput{ExpectedRevision: 1, Name: "Safety", Rules: []Rule{locked}}, http.StatusOK)
	must(admin, http.MethodPost, "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260929160000.0.0"}, http.StatusOK)
	merged := "/api/rules/merged?project_id=" + projectA + "&person_id=" + admin.ID + "&agent_id=" + coordinator.ID + "&role=coordinator&harness=codex"
	if body := must(coordinator, http.MethodGet, merged, nil, http.StatusOK); !strings.Contains(string(body), "Keep the locked company floor.") {
		t.Fatalf("merged rules missing company floor: %s", body)
	}
	status, raw = call(coordinator, http.MethodGet, "/api/rules/merged?project_id="+projectB+"&person_id="+admin.ID+"&agent_id="+coordinator.ID+"&role=coordinator&harness=codex", nil)
	if status != http.StatusForbidden {
		t.Fatalf("coordinator merged project B: %d %s", status, raw)
	}
	// Reproduce the creator ceiling leak with real comparison rows. The key's
	// role can read nodes workspace-wide; its creator can read only project A
	// nodes while retaining workspace rules.read.
	must(other, http.MethodPost, "/api/rules/comparisons", comparisonBody(projectA, "codex", "allowed-project-a"), http.StatusOK)
	must(other, http.MethodPost, "/api/rules/comparisons", comparisonBody(projectB, "codex", "private-project-b"), http.StatusOK)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,'nodes.read' FROM roles WHERE tenant_id=$1 AND key='coord_ws'`, tid)
	exec(`DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, admin.ID)
	var readerRole string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'creator_rules','Creator rules reader') RETURNING id::text`, tid).Scan(&readerRole); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'rules.read')`, tid, readerRole)
	exec(`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, admin.ID, readerRole)
	exec(`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3::uuid FROM roles WHERE tenant_id=$1 AND key='coord_a'`, tid, admin.ID, projectA)
	for _, scopes := range [][]string{authz.CoordinatorBaseScopes, authz.CoordinatorKeyScopes} {
		coordinator.Scopes = scopes
		if body := must(coordinator, http.MethodGet, "/api/rules/comparisons?project_id="+projectA, nil, http.StatusOK); !strings.Contains(string(body), "allowed-project-a") {
			t.Fatalf("allowed comparison missing: %s", body)
		}
		must(coordinator, http.MethodGet, "/api/rules/comparisons?project_id="+projectB, nil, http.StatusForbidden)
		must(coordinator, http.MethodGet, "/api/rules/sets?layer_id="+layerA.ID, nil, http.StatusOK)
		must(coordinator, http.MethodGet, "/api/rules/sets?layer_id="+layerB.ID, nil, http.StatusNotFound)
		must(coordinator, http.MethodGet, merged, nil, http.StatusOK)
		must(coordinator, http.MethodGet, "/api/rules/merged?project_id="+projectB+"&person_id="+admin.ID+"&agent_id="+coordinator.ID+"&role=coordinator&harness=codex", nil, http.StatusForbidden)
		body := must(coordinator, http.MethodGet, "/api/rules/layers", nil, http.StatusOK)
		if !strings.Contains(string(body), layerA.ID) || strings.Contains(string(body), layerB.ID) || strings.Contains(string(body), foreign.ID) {
			t.Fatalf("creator project/owner ceiling failed: %s", body)
		}
		// Bulk project decisions must agree with the handler's RequireTx path.
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			check, err := authz.ProjectsTx(ctx, tx, coordinator)
			if err != nil {
				return err
			}
			if !check("rules.read", projectA) || check("rules.read", projectB) || check("rules.read", "") {
				t.Error("ProjectsTx bypassed the creator project ceiling")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Removing the creator's rules permission revokes reads even though both
	// principals still have nodes.read on project A.
	exec(`DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='rules.read'`, tid, readerRole)
	must(coordinator, http.MethodGet, "/api/rules/layers", nil, http.StatusForbidden)
	exec(`INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'rules.read')`, tid, readerRole)
	must(coordinator, http.MethodGet, "/api/rules/layers", nil, http.StatusOK)

	// Node grants in different projects must not combine into an AnyProject
	// rules grant: the key retains A, while the creator now holds only B.
	exec(`DELETE FROM role_permissions WHERE tenant_id=$1 AND permission='nodes.read' AND role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='coord_ws')`, tid)
	exec(`UPDATE role_bindings SET scope_id=$3 WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='project'`, tid, admin.ID, projectB)
	must(coordinator, http.MethodGet, "/api/rules/layers", nil, http.StatusForbidden)
	must(coordinator, http.MethodGet, "/api/rules/comparisons?project_id="+projectA, nil, http.StatusForbidden)
	must(coordinator, http.MethodGet, "/api/rules/comparisons?project_id="+projectB, nil, http.StatusForbidden)
}
