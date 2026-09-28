// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestNextVersionStrictlyIncreases(t *testing.T) {
	now := time.Date(2026, 9, 28, 21, 30, 5, 0, time.UTC)
	if got := nextVersion(now, ""); got != "260928213005.0.0" {
		t.Fatal(got)
	}
	if got := nextVersion(now, "260928213005.0.0"); got != "260928213006.0.0" {
		t.Fatal("same second must move on:", got)
	}
	if got := nextVersion(now, "260928235959.0.0"); got != "260929000000.0.0" {
		t.Fatal("a later publication must be passed:", got)
	}
	a := batchIdentity(tenant.Principal{TenantID: "t", ID: "p"}, batchInput{Items: []batchItem{{"b", 2, "auto"}, {"a", 1, "auto"}}}, "n")
	b := batchIdentity(tenant.Principal{TenantID: "t", ID: "p"}, batchInput{Items: []batchItem{{"a", 1, "auto"}, {"b", 2, "auto"}}}, "n")
	c := batchIdentity(tenant.Principal{TenantID: "t", ID: "p"}, batchInput{Items: []batchItem{{"a", 1, "auto"}, {"b", 2, "auto"}}}, "other")
	if a != b || a == c || len(a) != 36 || a[14] != '8' {
		t.Fatal("batch id must be deterministic per request content", a, b, c)
	}
}

func TestBatchPublishIsOneAtomicPersonApproval(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-batch','Rules batch') RETURNING id::text`).Scan(&tid); err != nil {
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
	agent := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "builder", KeyCreatorID: admin.ID, Scopes: []string{"rules.read", "rules.write", "rules.publish", "nodes.read"}}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','builder') RETURNING id::text`, tid).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, agent.ID, "admin")
	var projectID string
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RBA-1','Project A' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectID)
	})
	if err != nil {
		t.Fatal(err)
	}
	// A project administrator may publish that project's rules, not the company's.
	scoped := person("project-owner", "member")
	if _, err = d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, scoped.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, tid, scoped.ID, projectID); err != nil {
		t.Fatal(err)
	}
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
	newSet := func(p tenant.Principal, layer Layer, name string, rules ...Rule) Set {
		t.Helper()
		var s Set
		if err := json.Unmarshal(call(p, "POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": name}, 200), &s); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(call(p, "PUT", "/api/rules/sets/"+s.ID+"/draft", draftInput{1, name, rules}, 200), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	var company, project, own Layer
	json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &company)
	json.Unmarshal(call(scoped, "POST", "/api/rules/layers", Scope{Layer: "project", ProjectID: projectID}, 200), &project)
	json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "person", OwnerID: admin.ID}, 200), &own)
	floor := testRule("safety", "Keep the locked company floor.")
	floor.Strength = "locked"
	safety := newSet(admin, company, "Safety", floor)
	git := newSet(admin, company, "Git", testRule("git-trash", "Delete with trash."))
	style := newSet(scoped, project, "Project style", testRule("project-style", "Use the project style."))
	mine := newSet(admin, own, "Pacing", testRule("pacing", "One step at a time."))

	events := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.published' `+where, append([]any{tid}, args...)...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	published := func(id string) string {
		t.Helper()
		var v string
		if err := d.Admin.QueryRow(t.Context(), `SELECT coalesce(fields->>'published_version','') FROM nodes WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	nothingWritten := func(label string) {
		t.Helper()
		if n := events(""); n != 0 {
			t.Fatalf("%s: %d publish events written", label, n)
		}
		for _, s := range []Set{safety, git, style, mine} {
			if v := published(s.ID); v != "" {
				t.Fatalf("%s: %s was published as %s", label, s.Name, v)
			}
		}
	}
	item := func(s Set, version string) map[string]any {
		return map[string]any{"set_id": s.ID, "expected_revision": s.Revision, "version": version}
	}
	batch := func(note string, items ...map[string]any) map[string]any {
		return map[string]any{"items": items, "note": note}
	}

	// Agents never publish, even with every scope and an admin binding.
	call(agent, "POST", "/api/rules/publish", batch("", item(safety, "auto")), 403)
	// Authorization is all or nothing and runs before any write: a project
	// administrator cannot slip a company set into a batch with their own set.
	call(scoped, "POST", "/api/rules/publish", batch("", item(style, "auto"), item(safety, "auto")), 403)
	call(member, "POST", "/api/rules/publish", batch("", item(safety, "auto")), 403)
	nothingWritten("denied")

	// A stale revision on one set leaves every set unpublished.
	stale := item(git, "auto")
	stale["expected_revision"] = git.Revision - 1
	call(admin, "POST", "/api/rules/publish", batch("", item(safety, "auto"), stale), 409)
	nothingWritten("revision conflict")
	// A failure after the first write (an explicit version that is not later
	// than one already stored) rolls the earlier writes back too.
	call(admin, "POST", "/api/rules/sets/"+mine.ID+"/publish", map[string]any{"expected_revision": mine.Revision, "version": "260928120000.0.0"}, 200)
	call(admin, "POST", "/api/rules/publish", batch("", item(safety, "260928120000.0.0"), item(mine, "260928110000.0.0")), 409)
	if v := published(safety.ID); v != "" {
		t.Fatal("a later failure left an earlier set published:", v)
	}
	if n := events(`AND after ? 'batch_id'`); n != 0 {
		t.Fatal("rolled back batch left events:", n)
	}
	call(admin, "POST", "/api/rules/publish", map[string]any{"items": []any{}}, 400)
	call(admin, "POST", "/api/rules/publish", batch("", item(safety, "auto"), item(safety, "auto")), 400)
	call(admin, "POST", "/api/rules/publish", batch("", item(safety, "next")), 400)
	call(admin, "POST", "/api/rules/publish", batch(strings.Repeat("a", maxPublishNote+1), item(safety, "auto")), 400)

	// One approval publishes every set, each with its own snapshot and event.
	req := batch("  First setup.  ", item(safety, "auto"), item(git, "auto"), item(style, "auto"))
	first := call(admin, "POST", "/api/rules/publish", req, 200)
	var got BatchResult
	if err = json.Unmarshal(first, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != 3 || got.BatchID == "" || got.MaxBytes <= 0 {
		t.Fatalf("batch result: %s", first)
	}
	for i, s := range []Set{safety, git, style} {
		v := got.Versions[i]
		if v.SetID != s.ID || v.Note != "First setup." || v.SHA256 != SnapshotDigest(v) || published(s.ID) != v.Version {
			t.Fatalf("set %s: %+v", s.Name, v)
		}
	}
	if n := events(`AND after->>'batch_id'=$2`, got.BatchID); n != 3 {
		t.Fatal("want one publish event per set with the batch id, got", n)
	}
	// An exact replay answers with the original result and writes nothing.
	replay := call(admin, "POST", "/api/rules/publish", req, 200)
	if !bytes.Equal(first, replay) {
		t.Fatalf("replay changed the answer\nfirst  %s\nreplay %s", first, replay)
	}
	if n := events(`AND after->>'batch_id'=$2`, got.BatchID); n != 3 {
		t.Fatal("replay wrote events:", n)
	}

	// The merged byte budget is enforced over the result, and a batch that
	// would exceed it writes nothing.
	long := []Rule{}
	for i := range 30 {
		long = append(long, testRule(fmt.Sprintf("long-%02d", i), strings.Repeat("Explain every step in full detail. ", 13)))
	}
	big := newSet(admin, company, "Everything", long...)
	gitNext := git
	var saved Set
	json.Unmarshal(call(admin, "PUT", "/api/rules/sets/"+git.ID+"/draft", draftInput{git.Revision, "Git", []Rule{testRule("git-trash", "Delete with trash, never rm.")}}, 200), &saved)
	gitNext.Revision = saved.Revision
	body := call(admin, "POST", "/api/rules/publish", batch("", item(gitNext, "auto"), item(big, "auto")), 422)
	var e Error
	if err = json.Unmarshal(body, &e); err != nil || e.Code != "rules_budget_exceeded" || e.ActualBytes <= MaxBytes || e.MaxBytes != MaxBytes {
		t.Fatalf("budget refusal: %s", body)
	}
	if published(big.ID) != "" || published(git.ID) != got.Versions[1].Version {
		t.Fatal("a refused batch changed a publication")
	}
	if n := events(""); n != 4 {
		t.Fatal("a refused batch wrote events; total", n)
	}
	// The single-set publication route is unchanged.
	call(admin, "POST", "/api/rules/sets/"+gitNext.ID+"/publish", map[string]any{"expected_revision": gitNext.Revision, "version": "261001000000.0.0"}, 200)
}
