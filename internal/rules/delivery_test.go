// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestSessionFileOmitsDoctrineAndKeepsBudgetsApart(t *testing.T) {
	const identity = "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets"
	bulk := strings.Repeat("D", 20000)
	commit := strings.Repeat("ab", 20)
	cat := doctrine.Catalog{
		Releases: []doctrine.Release{{Repository: "inspr-at/inspr-modules", Ref: "v260922101217.0.0", Commit: commit}},
		Rules: []doctrine.Indexed{
			{Identity: identity, Key: "no-env-dump", Text: "Never print the environment."},
			{Identity: "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#bulk", Key: "bulk", Text: bulk},
		},
	}
	byText := testRule("copied", "Never print the environment.")
	byID := testRule("aliased", "Say this another way.")
	byID.Source.Identity = identity
	kept := testRule("local", "Use the project voice.")
	m, err := MergeDelivered(testContext(), []Snapshot{
		floorSnapshot(),
		testSnapshot("project", Scope{Layer: "project", ProjectID: testProject}, byText, byID, kept),
	}, time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), cat)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Body, "Never print the environment.") || strings.Contains(m.Body, bulk) || strings.Contains(m.Body, "Say this another way.") {
		t.Fatalf("session file repeated doctrine\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "Use the project voice.") || !strings.Contains(m.Body, "Preserve safety.") {
		t.Fatalf("session file dropped an Aeon rule\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "inspr-at/inspr-modules@v260922101217.0.0 "+commit) || !strings.HasPrefix(m.Body, SessionHeader) {
		t.Fatalf("pointer\n%s", m.Body)
	}
	if m.ByteSize != len(m.Body) || m.ByteSize > MaxBytes || m.ByteSize >= len(bulk) {
		t.Fatalf("session budget counted doctrine bytes: %d body %d", m.ByteSize, len(m.Body))
	}
	for _, rule := range m.Rules {
		if rule.Identity == "copied" || rule.Identity == "aliased" {
			t.Fatalf("doctrine copy stayed in the rule list: %s", rule.Identity)
		}
	}
}

func TestPublishBlocksDoctrineDuplicateAndSessionOmitsIt(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-channel','Rules channel') RETURNING id::text`).Scan(&tid); err != nil {
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
	doc := []byte("# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- Never print the environment.\n  Why: transcripts keep it.\n")
	const repo = "inspr-at/fixture-doctrine"
	const commit = "1111111111111111111111111111111111111111"
	views := doctrine.Render(repo, commit, false, []doctrine.File{{Path: "docs/AGENTS-KERNEL.md", Content: doc}})
	if len(views) != 1 || len(views[0].Rules) != 1 || views[0].Rules[0].Text != "Never print the environment." {
		t.Fatalf("index %+v", views)
	}
	doctrineID := views[0].Rules[0].Identity

	var layer Layer
	json.Unmarshal(call("POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &layer)
	var set Set
	json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Safety"}, 200), &set)
	floor := testRule("safety", "Keep the locked company floor.")
	floor.Strength = "locked"
	call("PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{1, "Safety", []Rule{floor}}, 200)
	call("POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120000.0.0"}, 200)

	var projectID string
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'CH-1','Channel' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var projectLayer Layer
	json.Unmarshal(call("POST", "/api/rules/layers", Scope{Layer: "project", ProjectID: projectID}, 200), &projectLayer)
	var projectSet Set
	json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": projectLayer.ID, "name": "Project"}, 200), &projectSet)
	copied := testRule("copied", "Never print the environment.")
	call("PUT", "/api/rules/sets/"+projectSet.ID+"/draft", draftInput{1, "Project", []Rule{copied}}, 200)
	call("POST", "/api/rules/sets/"+projectSet.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120001.0.0"}, 200)

	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id, repository, visibility, ref, commit_sha, paths) VALUES($1,$2,'public','v260922101217.0.0',$3,ARRAY['docs/AGENTS-KERNEL.md']) RETURNING id::text`, tid, repo, commit).Scan(&id); e != nil {
			return e
		}
		if _, e := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id, source_id, commit_sha, path, blob_sha, content) VALUES($1,$2,$3,'docs/AGENTS-KERNEL.md',$4,$5)`, tid, id, commit, doctrine.BlobSHA(doc), doc); e != nil {
			return e
		}
		_, e := tx.Exec(t.Context(), `UPDATE doctrine_sources SET indexed_at=clock_timestamp() WHERE id=$1`, id)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	merged := call("GET", "/api/rules/merged?project_id="+projectID+"&person_id="+admin.ID+"&role=builder&harness=codex", nil, 200)
	if strings.Contains(string(merged), "Never print the environment.") || !strings.Contains(string(merged), "Keep the locked company floor") || !strings.Contains(string(merged), repo+"@v260922101217.0.0 "+commit) {
		t.Fatalf("merged session file\n%s", merged)
	}
	var body struct {
		ByteSize int    `json:"byte_size"`
		Body     string `json:"body"`
	}
	if err = json.Unmarshal(merged, &body); err != nil {
		t.Fatal(err)
	}
	if body.ByteSize != len(body.Body) || body.ByteSize > MaxBytes || strings.Contains(body.Body, string(doc)) {
		t.Fatalf("budget %d body includes doctrine file", body.ByteSize)
	}
	channels := call("GET", "/api/rules/channels", nil, 200)
	if !strings.Contains(string(channels), `"identity":"copied"`) || !strings.Contains(string(channels), doctrineID) || strings.Contains(string(channels), "Never print the environment.") {
		t.Fatalf("channels %s", channels)
	}
	call("PUT", "/api/rules/sets/"+projectSet.ID+"/draft", draftInput{2, "Project", []Rule{copied}}, 200)
	refused := call("POST", "/api/rules/sets/"+projectSet.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120002.0.0"}, 409)
	if !strings.Contains(string(refused), `"code":"doctrine_duplicate"`) || !strings.Contains(string(refused), "Propose a change") || !strings.Contains(string(refused), doctrineID) {
		t.Fatalf("publish %s", refused)
	}
	batch := call("POST", "/api/rules/publish", map[string]any{"items": []any{map[string]any{"set_id": projectSet.ID, "expected_revision": 3, "version": "260928120003.0.0"}}}, 409)
	if !strings.Contains(string(batch), "doctrine_duplicate") || !strings.Contains(string(batch), "Propose a change") {
		t.Fatalf("batch %s", batch)
	}
}
