// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const cmpDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const comparisonProse = "SYNTHETIC_RULE_PROSE_251"

func comparisonBody(project, harness, name string) map[string]any {
	return map[string]any{
		"harness": harness, "role": "builder", "project_id": project,
		"repo_sha256": cmpDigest, "repo_name": name,
		"merged": map[string]any{"version": "260929120000.0.0", "sha256": cmpDigest, "rule_count": 1},
		"local":  map[string]any{"set_sha256": cmpDigest, "rule_count": 1},
		"rules": []any{map[string]any{
			"identity": "safety", "status": "both",
			"local_text_sha256": cmpDigest, "merged_text_sha256": cmpDigest,
		}},
		"counts": map[string]any{"both": 1, "only_local": 0, "only_merged": 0, "differs": 0, "files": 1},
	}
}

func mustComparison(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := parseComparison(raw); err != nil {
		t.Fatal(err)
	}
}

func rejectedComparison(t *testing.T, raw []byte) {
	t.Helper()
	_, err := parseComparison(raw)
	var e *Error
	if !errors.As(err, &e) || e.Status != 400 || e.Message != "comparison rejected" || strings.Contains(err.Error(), comparisonProse) {
		t.Fatalf("%v", err)
	}
}

func TestParseComparisonRejectsProseAndCountDrift(t *testing.T) {
	good := comparisonBody("10000000-0000-4000-8000-000000000002", "codex", "fixture")
	mustComparison(t, jsonBytes(good))

	prose := comparisonBody("10000000-0000-4000-8000-000000000002", "codex", "fixture")
	prose["rules"] = []any{map[string]any{
		"identity": "safety", "status": "both", "text": comparisonProse,
		"local_text_sha256": cmpDigest, "merged_text_sha256": cmpDigest,
	}}
	raw := jsonBytes(prose)
	if !strings.Contains(string(raw), comparisonProse) {
		t.Fatal("fixture dropped the sentinel")
	}
	rejectedComparison(t, raw)

	drift := comparisonBody("10000000-0000-4000-8000-000000000002", "codex", "fixture")
	drift["counts"] = map[string]any{"both": 2, "only_local": 0, "only_merged": 0, "differs": 0, "files": 1}
	rejectedComparison(t, jsonBytes(drift))

	conflict := comparisonBody("10000000-0000-4000-8000-000000000002", "claude-code", "fixture")
	conflict["rules"] = []any{map[string]any{"identity": "safety", "status": "differs", "changed": []string{"local_conflict"}, "local_text_sha256": cmpDigest, "merged_text_sha256": cmpDigest}}
	conflict["counts"] = map[string]any{"both": 0, "only_local": 0, "only_merged": 0, "differs": 1, "files": 1}
	rejectedComparison(t, jsonBytes(conflict))
	conflict["rules"] = []any{map[string]any{"identity": "safety", "status": "differs", "changed": []string{"local_conflict"}}}
	mustComparison(t, jsonBytes(conflict))

	dup := comparisonBody("10000000-0000-4000-8000-000000000002", "codex", "fixture")
	dup["rules"] = []any{
		map[string]any{"identity": "safety", "status": "only_local", "local_text_sha256": cmpDigest},
		map[string]any{"identity": "safety", "status": "only_merged", "merged_text_sha256": cmpDigest},
	}
	dup["counts"] = map[string]any{"both": 0, "only_local": 1, "only_merged": 1, "differs": 0, "files": 1}
	dup["merged"] = map[string]any{"version": "260929120000.0.0", "sha256": cmpDigest, "rule_count": 1}
	dup["local"] = map[string]any{"set_sha256": cmpDigest, "rule_count": 1}
	rejectedComparison(t, jsonBytes(dup))
}

func TestComparisonsStoreHashesAndRefuseMutation(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-compare','Rules compare') RETURNING id::text`).Scan(&tid); err != nil {
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
		if role != "" {
			dbtest.BindRole(t, d, tid, p.ID, role)
		}
		return p
	}
	agentOf := func(name string, creator tenant.Principal, scopes []string) tenant.Principal {
		p := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: name, KeyCreatorID: creator.ID, Scopes: scopes}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent',$2) RETURNING id::text`, tid, name).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, p.ID, "member")
		return p
	}
	admin := person("owner", "admin")
	viewer := person("viewer", "viewer")
	unbound := person("unbound", "")
	reader := agentOf("reader", admin, []string{"rules.read"})
	writer := agentOf("writer", admin, []string{"rules.read", "rules.write"})
	var projectA, projectB string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if e := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'CMPA-1','Compare A' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectA); e != nil {
			return e
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'CMPB-1','Compare B' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectB)
	})
	if err != nil {
		t.Fatal(err)
	}
	scoped := person("project-owner", "member")
	if _, err = d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, scoped.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, tid, scoped.ID, projectA); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(p tenant.Principal, method, path string, in any, want int) []byte {
		t.Helper()
		body := ""
		if in != nil {
			if raw, ok := in.([]byte); ok {
				body = string(raw)
			} else {
				body = string(jsonBytes(in))
			}
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
	prose := comparisonBody(projectA, "codex", "fixture")
	prose["text"] = comparisonProse
	if strings.Contains(string(call(admin, "POST", "/api/rules/comparisons", prose, 400)), comparisonProse) {
		t.Fatal("rejection echoed prose")
	}
	drift := comparisonBody(projectA, "codex", "fixture")
	drift["counts"] = map[string]any{"both": 0, "only_local": 0, "only_merged": 0, "differs": 0, "files": 1}
	call(admin, "POST", "/api/rules/comparisons", drift, 400)
	call(admin, "POST", "/api/rules/comparisons?x=1", comparisonBody(projectA, "codex", "fixture"), 400)
	call(viewer, "GET", "/api/rules/comparisons?project_id="+projectA, nil, 403)
	call(viewer, "POST", "/api/rules/comparisons", comparisonBody(projectA, "codex", "fixture"), 403)
	call(unbound, "GET", "/api/rules/comparisons?project_id="+projectA, nil, 403)
	call(unbound, "POST", "/api/rules/comparisons", comparisonBody(projectA, "codex", "fixture"), 403)
	call(reader, "POST", "/api/rules/comparisons", comparisonBody(projectA, "codex", "fixture"), 403)

	var first comparisonView
	if err = json.Unmarshal(call(admin, "POST", "/api/rules/comparisons", comparisonBody(projectA, "codex", "alpha"), 200), &first); err != nil {
		t.Fatal(err)
	}
	var second comparisonView
	if err = json.Unmarshal(call(admin, "POST", "/api/rules/comparisons", comparisonBody(projectA, "codex", "beta"), 200), &second); err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID || strings.Contains(string(jsonBytes(second)), comparisonProse) {
		t.Fatalf("ids %s %s", first.ID, second.ID)
	}
	var listed struct {
		Comparisons []comparisonView `json:"comparisons"`
	}
	if err = json.Unmarshal(call(admin, "GET", "/api/rules/comparisons?project_id="+projectA, nil, 200), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Comparisons) != 1 || listed.Comparisons[0].ID != second.ID || listed.Comparisons[0].RepoName != "beta" || listed.Comparisons[0].Counts.Both != 1 {
		t.Fatalf("%+v", listed.Comparisons)
	}
	var foreign comparisonView
	if err = json.Unmarshal(call(admin, "POST", "/api/rules/comparisons", comparisonBody(projectB, "codex", "other"), 200), &foreign); err != nil {
		t.Fatal(err)
	}
	pageA := call(admin, "GET", "/api/rules/comparisons?project_id="+projectA, nil, 200)
	if strings.Contains(string(pageA), foreign.ID) || strings.Contains(string(pageA), projectB) {
		t.Fatal("project A listed project B")
	}
	call(scoped, "GET", "/api/rules/comparisons?project_id="+projectA, nil, 200)
	call(scoped, "GET", "/api/rules/comparisons?project_id="+projectB, nil, 403)
	call(admin, "GET", "/api/rules/comparisons?project_id=10000000-0000-4000-8000-000000000099", nil, 404)
	call(reader, "GET", "/api/rules/comparisons?project_id="+projectA, nil, 200)

	var stored comparisonView
	if err = json.Unmarshal(call(writer, "POST", "/api/rules/comparisons", comparisonBody(projectA, "claude-code", "agent"), 200), &stored); err != nil {
		t.Fatal(err)
	}
	var personID, agentID string
	if err = d.Admin.QueryRow(t.Context(), `SELECT person_id::text, agent_id::text FROM rules_comparisons WHERE id=$1`, stored.ID).Scan(&personID, &agentID); err != nil {
		t.Fatal(err)
	}
	if personID != admin.ID || agentID != writer.ID {
		t.Fatalf("person %s agent %s", personID, agentID)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE rules_comparisons SET repo_name='tamper' WHERE id=$1`, stored.ID)
		return e
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatal(err)
	}
	var events int
	if err = d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type LIKE 'rules.%'`, tid).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatal("comparison wrote a rules event")
	}
}
