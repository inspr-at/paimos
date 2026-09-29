// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestStoreAuthorizationHistoryAndGenericNodeGuards(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-fixture','Rules fixture') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	person := func(name, role string) tenant.Principal {
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: name}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, tid, name).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, p.ID, role)
		return p
	}
	admin := person("owner", "admin")
	member := person("member", "member")
	other := person("other", "admin")
	agent := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "builder", KeyCreatorID: admin.ID, Scopes: []string{"rules.read", "rules.write", "rules.publish", "nodes.read"}}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','builder') RETURNING id::text`, tid).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, agent.ID, "admin")
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(p tenant.Principal, method, path string, in any, want int) []byte {
		t.Helper()
		body := ""
		if in != nil {
			body = string(jsonBytes(in))
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var layer Layer
	json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &layer)
	call(agent, "POST", "/api/rules/layers", Scope{Layer: "company"}, 403)
	call(member, "POST", "/api/rules/layers", Scope{Layer: "company"}, 403)
	var set Set
	json.Unmarshal(call(admin, "POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Safety"}, 200), &set)
	locked := testRule("safety", "Keep the locked company floor.")
	locked.Strength = "locked"
	call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", map[string]any{"expected_revision": 1, "name": "Safety"}, 400)
	call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{1, "Safety", nil}, 400)
	call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{1, "Safety", []Rule{locked}}, 200)
	call(agent, "PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{2, "Safety", []Rule{locked}}, 403)
	pub := map[string]any{"expected_revision": 2, "version": "260928110000.0.0"}
	call(agent, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 403)
	call(member, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 403)
	first := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 200)
	replay := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 200)
	if string(first) != string(replay) {
		t.Fatal("publish replay changed snapshot")
	}
	locked.Text = "Updated safety."
	call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{2, "Safety", []Rule{locked}}, 200)
	pub["expected_revision"] = 3
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 409)
	pub["version"] = "260229110000.0.0"
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 400)
	pub["version"] = "260928110001.0.0"
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", pub, 200)
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/restore", map[string]any{"expected_revision": 3, "version": "260928110000.0.0", "new_version": "260928110002.0.0"}, 200)
	historical := call(admin, "GET", "/api/rules/sets/"+set.ID+"/versions/260928110000.0.0", nil, 200)
	if string(first) != string(historical) {
		t.Fatal("restore rewrote old version")
	}
	var private Layer
	json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "person", OwnerID: admin.ID}, 200), &private)
	var privSet Set
	json.Unmarshal(call(admin, "POST", "/api/rules/sets", map[string]any{"layer_id": private.ID, "name": "Private preferences"}, 200), &privSet)
	call(other, "GET", "/api/rules/sets/"+privSet.ID, nil, 404)
	call(other, "POST", "/api/rules/layers", Scope{Layer: "person", OwnerID: admin.ID}, 403)
	call(agent, "PUT", "/api/rules/sets/"+privSet.ID+"/draft", draftInput{1, "Private preferences", []Rule{testRule("style", "Be clear.")}}, 200)
	// Generic reads and writes cannot bypass private ownership, rules permission,
	// or version immutability, even for a workspace administrator.
	err = db.InTenant(tenant.WithPrincipal(t.Context(), other), d.App, tid, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=$1`, privSet.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("generic node read leaks private rules")
		}
		if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'rules.%'`).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("generic event read leaks rules")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(tenant.WithPrincipal(t.Context(), admin), d.App, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `SELECT set_config('aeon.rules_access','on',true),set_config('aeon.rules_projects','*',true),set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_write','on',true)`, admin.ID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(t.Context(), `UPDATE nodes SET title='tamper' WHERE parent_id=$1 AND rule_resource='version'`, set.ID)
		return e
	})
	if err == nil {
		t.Fatal("database allowed published snapshot mutation")
	}

	// Generic nodes cannot forge the reserved storage envelope to bypass publish.
	err = db.InTenant(tenant.WithPrincipal(t.Context(), admin), d.App, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'FORGE-1','forged','{"_aeon_rule_resource":"version"}'::jsonb FROM node_kinds WHERE tenant_id=$1 AND slug='ticket'`, tid)
		return e
	})
	if err == nil {
		t.Fatal("generic node forged rule snapshot")
	}
	var projectID, foreignProject string
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if e := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RPA-1','Project A' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectID); e != nil {
			return e
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RPB-1','Project B' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&foreignProject)
	})
	if err != nil {
		t.Fatal(err)
	}
	scoped := person("project-owner", "member")
	if _, err = d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, scoped.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, tid, scoped.ID, projectID); err != nil {
		t.Fatal(err)
	}
	var projectLayer Layer
	json.Unmarshal(call(scoped, "POST", "/api/rules/layers", Scope{Layer: "project", ProjectID: projectID}, 200), &projectLayer)
	call(scoped, "POST", "/api/rules/layers", Scope{Layer: "project", ProjectID: foreignProject}, 403)
	var projectSet Set
	json.Unmarshal(call(scoped, "POST", "/api/rules/sets", map[string]any{"layer_id": projectLayer.ID, "name": "Project rules"}, 200), &projectSet)
	call(scoped, "PUT", "/api/rules/sets/"+projectSet.ID+"/draft", draftInput{1, "Project rules", []Rule{testRule("project-style", "Use project style.")}}, 200)
	call(scoped, "POST", "/api/rules/sets/"+projectSet.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928110003.0.0"}, 200)
	mergePath := "/api/rules/merged?project_id=" + projectID + "&person_id=" + scoped.ID + "&role=builder&harness=codex"
	merged := call(scoped, "GET", mergePath, nil, 200)
	if !strings.Contains(string(merged), "Use project style") || !strings.Contains(string(merged), "Keep the locked company floor") {
		t.Fatal("project member did not receive company + project rules")
	}
	call(scoped, "GET", strings.Replace(mergePath, projectID, foreignProject, 1), nil, 403)
	call(scoped, "GET", strings.Replace(mergePath, scoped.ID, other.ID, 1), nil, 403)
	call(scoped, "GET", mergePath+"&role=reviewer", nil, 400)
	call(scoped, "GET", strings.Replace(mergePath, "role=builder", "role=unknown", 1), nil, 400)
	call(agent, "GET", "/api/rules/merged?project_id="+projectID+"&person_id="+admin.ID+"&agent_id="+other.ID+"&role=builder&harness=codex", nil, 403)

	// The marker column cannot be spoofed on an ordinary node.
	err = db.InTenant(tenant.WithPrincipal(t.Context(), admin), d.App, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE nodes SET rule_resource='version' WHERE id=$1`, projectID)
		return e
	})
	if err == nil {
		t.Fatal("ordinary node spoofed rule marker")
	}
	// Named-agent ownership is tied to an actual active creator key, not an
	// arbitrary owner_id supplied in a request. This key row is synthetic only.
	if _, err = d.Admin.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'rules-fixture','rules-fixture-prefix','fixture-not-a-credential',$3)`, tid, agent.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	var named Layer
	json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "agent", OwnerID: admin.ID, AgentID: agent.ID}, 200), &named)
	var namedSet Set
	json.Unmarshal(call(admin, "POST", "/api/rules/sets", map[string]any{"layer_id": named.ID, "name": "Named agent"}, 200), &namedSet)
	call(agent, "PUT", "/api/rules/sets/"+namedSet.ID+"/draft", draftInput{1, "Named agent", []Rule{testRule("persona", "Agent-specific rule.")}}, 200)
	call(admin, "POST", "/api/rules/sets/"+namedSet.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928110004.0.0"}, 200)
	call(other, "GET", "/api/rules/sets/"+namedSet.ID, nil, 404)
	secondAgent := agent
	secondAgent.Name = "other-agent"
	if err = d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','other-agent') RETURNING id::text`, tid).Scan(&secondAgent.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, secondAgent.ID, "admin")
	call(secondAgent, "GET", "/api/rules/sets/"+namedSet.ID, nil, 404)
	for _, project := range []string{projectID, foreignProject} {
		out := call(agent, "GET", "/api/rules/merged?project_id="+project+"&person_id="+admin.ID+"&agent_id="+agent.ID+"&role=builder&harness=codex", nil, 200)
		if !strings.Contains(string(out), "Agent-specific rule") {
			t.Fatal("named rules did not follow agent between projects")
		}
	}
	// A foreign tenant cannot obtain a known UUID.
	var foreign string
	if err = d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-foreign','Foreign') RETURNING id::text`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	fp := tenant.Principal{TenantID: foreign, Kind: tenant.Person, Name: "foreign-admin"}
	if err = d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','foreign-admin') RETURNING id::text`, foreign).Scan(&fp.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, foreign, fp.ID, "admin")
	call(fp, "GET", "/api/rules/sets/"+set.ID, nil, 404)
	call(fp, "GET", "/api/rules/sets/"+privSet.ID, nil, 404)
	// A valid foreign-tenant administrator cannot see local rule layers either.
	foreignLayers := call(fp, "GET", "/api/rules/layers", nil, 200)
	if strings.Contains(string(foreignLayers), layer.ID) {
		t.Fatal("foreign tenant layer leak")
	}
	savedRules := maxBudgetRules
	maxBudgetRules = 1
	t.Cleanup(func() { maxBudgetRules = savedRules })
	over := call(scoped, "GET", mergePath, nil, 422)
	if !strings.Contains(string(over), `"code":"budget_check_too_large"`) {
		t.Fatalf("store cap: %s", over)
	}
	maxBudgetRules = savedRules
	var restoredFrom string
	if err = d.Admin.QueryRow(t.Context(), `SELECT before->>'source_version' FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='rules.restored'`, tid, set.ID).Scan(&restoredFrom); err != nil || restoredFrom != "260928110000.0.0" {
		t.Fatal("missing restoration audit", err)
	}
}

func TestMergedLoadStopsWhenTheStoreCapIsCrossed(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-stopload','Rules stop load') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	admin := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: "owner"}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','owner') RETURNING id::text`, tid).Scan(&admin.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, admin.ID, "admin")
	var projectID string
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RSTOP-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectID)
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(method, path string, in any, want int) []byte {
		t.Helper()
		body := ""
		if in != nil {
			body = string(jsonBytes(in))
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), admin))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var layer Layer
	if err = json.Unmarshal(call("POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &layer); err != nil {
		t.Fatal(err)
	}
	locked := testRule("safety", "Keep the locked company floor.")
	locked.Strength = "locked"
	rules := []Rule{locked, testRule("tone", "Be brief."), testRule("format", "Use plain sentences.")}
	names := []string{"Safety", "Tone", "Format"}
	for i, rule := range rules {
		var set Set
		if err = json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": names[i]}, 200), &set); err != nil {
			t.Fatal(err)
		}
		call("PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{1, names[i], []Rule{rule}}, 200)
		call("POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260929084800.0.0"}, 200)
	}
	var ordered []string
	rows, err := d.Admin.Query(t.Context(), `SELECT id::text FROM nodes WHERE tenant_id=$1 AND rule_resource='set' AND deleted_at IS NULL ORDER BY id`, tid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ordered = append(ordered, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(ordered) != 3 {
		t.Fatal(err, ordered)
	}
	var loaded []string
	savedHook := beforeSnapshotLoad
	savedRules, savedBytes := maxBudgetRules, maxBudgetBytes
	beforeSnapshotLoad = func(setID, _ string) { loaded = append(loaded, setID) }
	t.Cleanup(func() {
		beforeSnapshotLoad = savedHook
		maxBudgetRules, maxBudgetBytes = savedRules, savedBytes
	})
	path := "/api/rules/merged?project_id=" + projectID + "&person_id=" + admin.ID + "&role=builder&harness=codex"
	merge := func(want int) []byte {
		t.Helper()
		loaded = nil
		return call("GET", path, nil, want)
	}
	if body := merge(200); !strings.Contains(string(body), "Keep the locked company floor") || len(loaded) != 3 || loaded[0] != ordered[0] || loaded[1] != ordered[1] || loaded[2] != ordered[2] {
		t.Fatalf("under cap loaded %v ordered %v body %s", loaded, ordered, body)
	}
	maxBudgetRules = 2
	over := merge(422)
	if !strings.Contains(string(over), `"code":"budget_check_too_large"`) || len(loaded) != 2 || loaded[0] != ordered[0] || loaded[1] != ordered[1] {
		t.Fatalf("rule cap loaded %v ordered %v body %s", loaded, ordered, over)
	}
	maxBudgetRules = savedRules
	maxBudgetBytes = 1
	over = merge(422)
	if !strings.Contains(string(over), `"code":"budget_check_too_large"`) || len(loaded) != 1 || loaded[0] != ordered[0] {
		t.Fatalf("byte cap loaded %v ordered %v body %s", loaded, ordered, over)
	}
}
