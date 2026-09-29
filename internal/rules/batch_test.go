// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNextVersionAndBatchIdentity(t *testing.T) {
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
	a := requestDigest("t", "p", []batchItem{{"b", 2, "auto"}, {"a", 1, "auto"}}, "n")
	b := requestDigest("t", "p", []batchItem{{"a", 1, "auto"}, {"b", 2, "auto"}}, "n")
	c := requestDigest("t", "p", []batchItem{{"a", 1, "auto"}, {"b", 2, "auto"}}, "other")
	d := requestDigest("t", "q", []batchItem{{"a", 1, "auto"}, {"b", 2, "auto"}}, "n")
	if !bytes.Equal(a, b) || bytes.Equal(a, c) || bytes.Equal(a, d) {
		t.Fatal("request identity must follow set order-free content, note and person")
	}
	if id := uuidFrom(a); len(id) != 36 || id[14] != '8' {
		t.Fatal("batch id is not a version 8 UUID:", id)
	}
}

// batchWorld is one tenant with people, an agent, a project and the rules API.
type batchWorld struct {
	t       testing.TB
	d       *dbtest.DB
	tid     string
	mux     *http.ServeMux
	project string
}

func newBatchWorld(t testing.TB, slug string) *batchWorld {
	d := dbtest.Open(t)
	w := &batchWorld{t: t, d: d, mux: http.NewServeMux()}
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Rules batch') RETURNING id::text`, slug).Scan(&w.tid); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, w.tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RBA-1','Project A' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, w.tid).Scan(&w.project)
	})
	if err != nil {
		t.Fatal(err)
	}
	New(d.App).Mount(w.mux)
	return w
}
func (w *batchWorld) principal(kind tenant.PrincipalKind, name, role string) tenant.Principal {
	w.t.Helper()
	p := tenant.Principal{TenantID: w.tid, Kind: kind, Name: name}
	err := db.InTenant(dbtest.Seed(w.t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
		return tx.QueryRow(w.t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, w.tid, string(kind), name).Scan(&p.ID)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	dbtest.BindRole(w.t, w.d, w.tid, p.ID, role)
	return p
}
func (w *batchWorld) send(p tenant.Principal, method, path string, in any) (int, []byte) {
	body := ""
	if in != nil {
		body = string(jsonBytes(in))
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}
func (w *batchWorld) call(p tenant.Principal, method, path string, in any, want int) []byte {
	w.t.Helper()
	code, body := w.send(p, method, path, in)
	if code != want {
		w.t.Fatalf("%s %s: status %d want %d: %s", method, path, code, want, body)
	}
	return body
}
func (w *batchWorld) layer(p tenant.Principal, s Scope) Layer {
	w.t.Helper()
	var l Layer
	if err := json.Unmarshal(w.call(p, "POST", "/api/rules/layers", s, 200), &l); err != nil {
		w.t.Fatal(err)
	}
	return l
}
func (w *batchWorld) set(p tenant.Principal, l Layer, name string, rules ...Rule) Set {
	w.t.Helper()
	var s Set
	if err := json.Unmarshal(w.call(p, "POST", "/api/rules/sets", map[string]any{"layer_id": l.ID, "name": name}, 200), &s); err != nil {
		w.t.Fatal(err)
	}
	if err := json.Unmarshal(w.call(p, "PUT", "/api/rules/sets/"+s.ID+"/draft", draftInput{ExpectedRevision: 1, Name: name, Rules: rules}, 200), &s); err != nil {
		w.t.Fatal(err)
	}
	return s
}
func (w *batchWorld) publish(p tenant.Principal, s Set, version string) {
	w.t.Helper()
	w.call(p, "POST", "/api/rules/sets/"+s.ID+"/publish", map[string]any{"expected_revision": s.Revision, "version": version}, 200)
}
func (w *batchWorld) events(where string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.d.Admin.QueryRow(w.t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.published' `+where, append([]any{w.tid}, args...)...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}
func (w *batchWorld) published(id string) string {
	w.t.Helper()
	var v string
	if err := w.d.Admin.QueryRow(w.t.Context(), `SELECT coalesce(fields->>'published_version','') FROM nodes WHERE id=$1`, id).Scan(&v); err != nil {
		w.t.Fatal(err)
	}
	return v
}
func item(s Set, version string) map[string]any {
	return map[string]any{"set_id": s.ID, "expected_revision": s.Revision, "version": version}
}
func batch(note string, items ...map[string]any) map[string]any {
	return map[string]any{"items": items, "note": note}
}
func lockedRule(id, text string) Rule {
	r := testRule(id, text)
	r.Strength = "locked"
	return r
}

// bulky returns n rules of about 470 bytes each in the rendered file.
func bulky(prefix string, n int) []Rule {
	out := []Rule{}
	for i := range n {
		out = append(out, testRule(fmt.Sprintf("%s-%02d", prefix, i), strings.Repeat("Explain every step in full detail. ", 13)))
	}
	return out
}

func TestBatchPublishIsOneAtomicPersonApproval(t *testing.T) {
	w := newBatchWorld(t, "rules-batch")
	admin := w.principal(tenant.Person, "owner", "admin")
	member := w.principal(tenant.Person, "member", "member")
	agent := w.principal(tenant.Agent, "builder", "admin")
	agent.KeyCreatorID = admin.ID
	agent.Scopes = []string{"rules.read", "rules.write", "rules.publish", "nodes.read"}
	// A project administrator may publish that project's rules, not the company's.
	scoped := w.principal(tenant.Person, "project-owner", "member")
	if _, err := w.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, w.tid, scoped.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, w.tid, scoped.ID, w.project); err != nil {
		t.Fatal(err)
	}
	company := w.layer(admin, Scope{Layer: "company"})
	project := w.layer(scoped, Scope{Layer: "project", ProjectID: w.project})
	own := w.layer(admin, Scope{Layer: "person", OwnerID: admin.ID})
	safety := w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor."))
	git := w.set(admin, company, "Git", testRule("git-trash", "Delete with trash."))
	style := w.set(scoped, project, "Project style", testRule("project-style", "Use the project style."))
	mine := w.set(admin, own, "Pacing", testRule("pacing", "One step at a time."))
	nothingWritten := func(label string) {
		t.Helper()
		if n := w.events(""); n != 0 {
			t.Fatalf("%s: %d publish events written", label, n)
		}
		for _, s := range []Set{safety, git, style, mine} {
			if v := w.published(s.ID); v != "" {
				t.Fatalf("%s: %s was published as %s", label, s.Name, v)
			}
		}
	}

	// Agents never publish, even with every scope and an admin binding.
	w.call(agent, "POST", "/api/rules/publish", batch("", item(safety, "auto")), 403)
	// Authorization is all or nothing and runs before any write: a project
	// administrator cannot slip a company set into a batch with their own set.
	w.call(scoped, "POST", "/api/rules/publish", batch("", item(style, "auto"), item(safety, "auto")), 403)
	w.call(member, "POST", "/api/rules/publish", batch("", item(safety, "auto")), 403)
	nothingWritten("denied")

	// A stale revision on one set leaves every set unpublished.
	stale := item(git, "auto")
	stale["expected_revision"] = git.Revision - 1
	w.call(admin, "POST", "/api/rules/publish", batch("", item(safety, "auto"), stale), 409)
	nothingWritten("revision conflict")
	// A failure after the first write (an explicit version that is not later
	// than one already stored) rolls the earlier writes back too.
	w.publish(admin, mine, "260928120000.0.0")
	w.call(admin, "POST", "/api/rules/publish", batch("", item(safety, "260928120000.0.0"), item(mine, "260928110000.0.0")), 409)
	if v := w.published(safety.ID); v != "" {
		t.Fatal("a later failure left an earlier set published:", v)
	}
	if n := w.events(`AND after ? 'batch_id'`); n != 0 {
		t.Fatal("rolled back batch left events:", n)
	}
	// A version already stored is never reused by a new request.
	w.call(admin, "POST", "/api/rules/publish", batch("", item(mine, "260928120000.0.0")), 409)
	w.call(admin, "POST", "/api/rules/publish", map[string]any{"items": []any{}}, 400)
	w.call(admin, "POST", "/api/rules/publish", batch("", item(safety, "auto"), item(safety, "auto")), 400)
	w.call(admin, "POST", "/api/rules/publish", batch("", item(safety, "next")), 400)
	w.call(admin, "POST", "/api/rules/publish", batch(strings.Repeat("a", maxPublishNote+1), item(safety, "auto")), 400)
	// Case variants of one id are refused, so one set cannot appear twice or be
	// versioned backwards within a batch.
	upper := item(safety, "260928130002.0.0")
	upper["set_id"] = strings.ToUpper(safety.ID)
	w.call(admin, "POST", "/api/rules/publish", batch("", item(safety, "260928130001.0.0"), upper), 400)
	w.call(admin, "POST", "/api/rules/publish", batch("", upper), 400)
	if v := w.published(safety.ID); v != "" {
		t.Fatal("a refused case variant published:", v)
	}

	// One approval publishes every set, each with its own snapshot and event.
	req := batch("  First setup.  ", item(safety, "auto"), item(git, "auto"), item(style, "auto"))
	first := w.call(admin, "POST", "/api/rules/publish", req, 200)
	var got BatchResult
	if err := json.Unmarshal(first, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != 3 || got.BatchID == "" || got.MaxBytes <= 0 {
		t.Fatalf("batch result: %s", first)
	}
	for i, s := range []Set{safety, git, style} {
		v := got.Versions[i]
		if v.SetID != s.ID || v.Note != "First setup." || v.SHA256 != SnapshotDigest(v) || w.published(s.ID) != v.Version {
			t.Fatalf("set %s: %+v", s.Name, v)
		}
	}
	if n := w.events(`AND after->>'batch_id'=$2`, got.BatchID); n != 3 {
		t.Fatal("want one publish event per set with the batch id, got", n)
	}
	// An exact replay answers with the stored result and writes nothing.
	replay := w.call(admin, "POST", "/api/rules/publish", req, 200)
	if !bytes.Equal(first, replay) {
		t.Fatalf("replay changed the answer\nfirst  %s\nreplay %s", first, replay)
	}
	// The same revisions with another note are a new request: new versions.
	time.Sleep(1100 * time.Millisecond)
	second := w.call(admin, "POST", "/api/rules/publish", batch("Second note.", item(safety, "auto")), 200)
	var noted BatchResult
	if err := json.Unmarshal(second, &noted); err != nil || noted.BatchID == got.BatchID || noted.Versions[0].Version <= got.Versions[0].Version {
		t.Fatalf("a second note did not publish anew: %s", second)
	}
	// Replaying the first request now still returns its stored answer, with
	// no further version or event under its batch id.
	if again := w.call(admin, "POST", "/api/rules/publish", req, 200); !bytes.Equal(first, again) {
		t.Fatalf("replay after a newer publication changed\nfirst %s\nagain %s", first, again)
	}
	if n := w.events(`AND after->>'batch_id'=$2`, got.BatchID); n != 3 {
		t.Fatal("replay wrote events under the original batch id:", n)
	}
	if w.published(safety.ID) != noted.Versions[0].Version {
		t.Fatal("replay moved the live version back")
	}
	// Another person's identical request is not a replay of the first one: it
	// is authorized for itself and publishes its own versions.
	other := w.principal(tenant.Person, "second-owner", "admin")
	theirs := w.call(other, "POST", "/api/rules/publish", req, 200)
	var their BatchResult
	if err := json.Unmarshal(theirs, &their); err != nil || their.BatchID == got.BatchID {
		t.Fatalf("another person's request was answered with the first result: %s", theirs)
	}
	if n := w.events(`AND after->>'batch_id'=$2`, their.BatchID); n != 3 {
		t.Fatal("another person's request did not publish its own versions:", n)
	}
	// Replay re-checks authorization: once demoted, the stored answer is refused.
	if _, err := w.d.Admin.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member') WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='workspace'`, w.tid, admin.ID); err != nil {
		t.Fatal(err)
	}
	w.call(admin, "POST", "/api/rules/publish", req, 403)
	// Stored answers are private to their person, even for generic reads.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), other), w.d.App, w.tid, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM rule_publish_batches`).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("stored batch answers are readable outside the rules API")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The single-set publication route is unchanged.
	var fresh Set
	json.Unmarshal(w.call(other, "GET", "/api/rules/sets/"+git.ID, nil, 200), &fresh)
	w.call(other, "POST", "/api/rules/sets/"+git.ID+"/publish", map[string]any{"expected_revision": fresh.Revision, "version": "261001000000.0.0"}, 200)
}

// A demotion that commits while a batch waits is seen before anything is
// written: the batch waits for the access lock, then checks current grants.
func TestBatchPublishWaitsForAConcurrentDemotion(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-race")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.layer(admin, Scope{Layer: "company"})
	safety := w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor."))
	for _, single := range []bool{false, true} {
		// An access change in flight: it holds the tenant row, as authz does.
		demotion, err := w.d.Admin.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = demotion.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, w.tid); err != nil {
			t.Fatal(err)
		}
		if _, err = demotion.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member') WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='workspace'`, w.tid, admin.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan int, 1)
		go func() {
			path, body := "/api/rules/publish", any(batch("", item(safety, "auto")))
			if single {
				path, body = "/api/rules/sets/"+safety.ID+"/publish", map[string]any{"expected_revision": safety.Revision, "version": "260928140000.0.0"}
			}
			code, _ := w.send(admin, "POST", path, body)
			done <- code
		}()
		select {
		case code := <-done:
			t.Fatalf("single=%v: publication did not wait for the access change (status %d)", single, code)
		case <-time.After(400 * time.Millisecond):
		}
		if err = demotion.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if code := <-done; code != 403 {
			t.Fatalf("single=%v: publication after a committed demotion: status %d, want 403", single, code)
		}
		if v := w.published(safety.ID); v != "" {
			t.Fatal("a demoted person published:", v)
		}
		dbtest.BindRole(t, w.d, w.tid, admin.ID, "admin")
		if _, err = w.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2 AND role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member')`, w.tid, admin.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// budgetWorld adds the helpers the budget tests share.
func (w *batchWorld) floor(admin tenant.Principal) Layer {
	company := w.layer(admin, Scope{Layer: "company"})
	w.publish(admin, w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor.")), "260928090000.0.0")
	return company
}
func (w *batchWorld) agentFor(owner tenant.Principal, name string) tenant.Principal {
	w.t.Helper()
	a := w.principal(tenant.Agent, name, "member")
	if _, err := w.d.Admin.Exec(w.t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'rules-fixture',$3,'fixture-not-a-credential',$4)`, w.tid, a.ID, "rules-fixture-"+name, owner.ID); err != nil {
		w.t.Fatal(err)
	}
	return a
}
func (w *batchWorld) task() string {
	w.t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(w.t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
		return tx.QueryRow(w.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'RBT-1','A task',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='task' RETURNING id::text`, w.tid, w.project).Scan(&id)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}
func noContent(t testing.TB, label string, body []byte) {
	t.Helper()
	for _, secret := range []string{"Explain every step", "person-", "agent-", "task-"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("%s: the answer leaks rule content: %s", label, body)
		}
	}
}

// Refusals for a file the caller may read say how big it would be.
func (w *batchWorld) refusedOwn(admin tenant.Principal, label string, s Set) {
	w.t.Helper()
	body := w.call(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")), 422)
	var e Error
	if err := json.Unmarshal(body, &e); err != nil || e.Code != "rules_budget_exceeded" || e.ActualBytes <= MaxBytes || e.MaxBytes != MaxBytes {
		w.t.Fatalf("%s: %s", label, body)
	}
	noContent(w.t, label, body)
	if v := w.published(s.ID); v != "" {
		w.t.Fatalf("%s: published anyway as %s", label, v)
	}
}

// Refusals for another person's or agent's file say neither size nor whose.
func (w *batchWorld) refusedHidden(p tenant.Principal, label string, s Set) {
	w.t.Helper()
	body := w.call(p, "POST", "/api/rules/publish", batch("", item(s, "auto")), 422)
	var e map[string]any
	if err := json.Unmarshal(body, &e); err != nil || e["code"] != "rules_budget_exceeded" || e["error"] != errHiddenBudget.Message {
		w.t.Fatalf("%s: %s", label, body)
	}
	if _, sized := e["actual_bytes"]; sized || strings.ContainsAny(e["error"].(string), "0123456789") {
		w.t.Fatalf("%s: the refusal reveals a size: %s", label, body)
	}
	noContent(w.t, label, body)
	if v := w.published(s.ID); v != "" {
		w.t.Fatalf("%s: published anyway as %s", label, v)
	}
}

// The byte budget covers every file a publication changes, with every
// existing contribution, including layers the publishing person cannot see.
func TestBatchBudgetCoversEveryAffectedContext(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-budget")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)

	// (a) Aeon's project rules switch the big role rules off, but a project
	// without project rules would receive all of them.
	project := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	off := bulky("role", 28)
	for i := range off {
		off[i].Enabled = false
	}
	w.publish(admin, w.set(admin, project, "Quiet", off...), "260928090001.0.0")
	role := w.layer(admin, Scope{Layer: "agent", Role: "builder"})
	w.refusedOwn(admin, "project without project rules", w.set(admin, role, "Loud", bulky("role", 28)...))

	// (b) An existing named-agent contribution counts although its set is not
	// in the batch: 7 KB of agent rules plus 6 KB of new company rules.
	worker := w.agentFor(admin, "worker")
	named := w.layer(admin, Scope{Layer: "agent", OwnerID: admin.ID, AgentID: worker.ID})
	w.publish(admin, w.set(admin, named, "Persona", bulky("agent", 15)...), "260928090002.0.0")
	w.refusedOwn(admin, "existing named agent", w.set(admin, company, "Six", bulky("company", 13)...))

	// (b, task) A task contribution counts in its own project: 8 KB of task
	// rules plus 4.5 KB of company rules. Without the task the file fits.
	taskAgent := w.agentFor(admin, "tasker")
	taskID := w.task()
	taskLayer := w.layer(admin, Scope{Layer: "agent", OwnerID: admin.ID, AgentID: taskAgent.ID, ProjectID: w.project, TaskID: taskID})
	w.publish(admin, w.set(admin, taskLayer, "Task", bulky("task", 17)...), "260928090003.0.0")
	w.refusedOwn(admin, "existing task", w.set(admin, company, "FourHalf", bulky("companyt", 10)...))
}

// Other owners' private contributions count, but their sizes never reach the
// caller, and they never veto a publication that does not touch their files.
func TestBatchBudgetKeepsOtherOwnersPrivate(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-private")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	colleague := w.principal(tenant.Person, "colleague", "admin")
	private := w.layer(colleague, Scope{Layer: "person", OwnerID: colleague.ID})
	w.publish(colleague, w.set(colleague, private, "Mine", bulky("person", 19)...), "260928090004.0.0")

	// Success: the colleague's 9 KB file still fits next to a short company
	// rule, and the answer (and the stored replay) reports only the caller's sizes.
	short := w.set(admin, company, "Short", testRule("short", "A short rule."))
	req := batch("", item(short, "auto"))
	first := w.call(admin, "POST", "/api/rules/publish", req, 200)
	var ok BatchResult
	if err := json.Unmarshal(first, &ok); err != nil || ok.MaxBytes <= 0 || ok.MaxBytes > 1000 {
		t.Fatalf("max_bytes reflects someone else's file: %s", first)
	}
	if replay := w.call(admin, "POST", "/api/rules/publish", req, 200); !bytes.Equal(first, replay) {
		t.Fatalf("stored answer differs: %s", replay)
	}
	var stored []byte
	if err := w.d.Admin.QueryRow(t.Context(), `SELECT result::text FROM rule_publish_batches WHERE tenant_id=$1 AND batch_id=$2`, w.tid, ok.BatchID).Scan(&stored); err != nil || !strings.Contains(string(stored), fmt.Sprintf(`"max_bytes": %d`, ok.MaxBytes)) {
		t.Fatalf("stored max_bytes: %s %v", stored, err)
	}

	// Failure: 3.5 KB more company rules push the colleague's file over; the
	// caller learns only that some other file would not fit.
	w.refusedHidden(admin, "another owner's private rules", w.set(admin, company, "ThreeHalf", bulky("companyp", 8)...))

	// Another owner's named agent counts the same way.
	helper := w.agentFor(colleague, "helper")
	theirs := w.layer(colleague, Scope{Layer: "agent", OwnerID: colleague.ID, AgentID: helper.ID})
	w.publish(colleague, w.set(colleague, theirs, "Helper", bulky("agent", 2)...), "260928090005.0.0")
	w.refusedHidden(admin, "another owner's agent", w.set(admin, company, "Three", bulky("companya", 6)...))

	// Unrelated: the colleague's own file is oversized already (single publish
	// does not check the budget), yet the admin's person rules do not touch it.
	w.publish(colleague, w.set(colleague, private, "More", bulky("more", 8)...), "260928090006.0.0")
	own := w.layer(admin, Scope{Layer: "person", OwnerID: admin.ID})
	w.call(admin, "POST", "/api/rules/publish", batch("", item(w.set(admin, own, "Pacing", testRule("pacing", "One step at a time.")), "auto")), 200)
	// A company rule touches everyone's file, so it is refused, generically.
	w.refusedHidden(admin, "company rule over an oversized private file", w.set(admin, company, "Tiny", testRule("tiny", "Tiny.")))
}

func TestBatchBudgetCheckIsBounded(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-bounded")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	saved := maxBudgetContexts
	maxBudgetContexts = 5
	t.Cleanup(func() { maxBudgetContexts = saved })
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	body := w.call(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")), 422)
	if !strings.Contains(string(body), `"budget_check_too_large"`) || w.published(s.ID) != "" {
		t.Fatalf("an oversized check was not refused cleanly: %s", body)
	}
}

// The owner switch of the budget check is undone inside the same transaction:
// right after it, the caller sees exactly what they saw before.
func TestBudgetCheckRestoresVisibilityInTheTransaction(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-restore")
	admin := w.principal(tenant.Person, "owner", "admin")
	w.floor(admin)
	colleague := w.principal(tenant.Person, "colleague", "admin")
	private := w.layer(colleague, Scope{Layer: "person", OwnerID: colleague.ID})
	hidden := w.set(colleague, private, "Mine", testRule("mine", "Private."))
	w.publish(colleague, hidden, "260928090007.0.0")
	err := db.InTenant(tenant.WithPrincipal(t.Context(), admin), w.d.App, w.tid, func(tx pgx.Tx) error {
		projects := "{" + w.project + "}"
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.rules_access','on',true),set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_projects',$2,true),set_config('aeon.visible_projects','*',true)`, admin.ID, projects); err != nil {
			return err
		}
		company, err := allSets(t.Context(), tx, "")
		if err != nil {
			return err
		}
		if _, err = budgetCheck(t.Context(), tx, admin, admin.ID, company, time.Now(), DefaultBudget()); err != nil {
			return err
		}
		var owner, seenProjects string
		if err = tx.QueryRow(t.Context(), `SELECT current_setting('aeon.rules_owner'),current_setting('aeon.rules_projects')`).Scan(&owner, &seenProjects); err != nil {
			return err
		}
		if owner != admin.ID || seenProjects != projects {
			t.Fatalf("settings not restored: owner %q projects %q", owner, seenProjects)
		}
		var n int
		if err = tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=$1`, hidden.ID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("the colleague's private set is still visible after the check")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A stalled upload holds no lock: the body is read before the transaction, so
// an access change can take the tenant row meanwhile.
func TestRulesWriteReadsTheBodyBeforeLocking(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-body")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.layer(admin, Scope{Layer: "company"})
	s := w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor."))
	reader, writer := io.Pipe()
	req := httptest.NewRequest("PUT", "/api/rules/sets/"+s.ID+"/draft", reader)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), admin))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { w.mux.ServeHTTP(rec, req); close(done) }()
	if _, err := writer.Write([]byte(`{"expected_revision":`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	access, err := w.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Exec(t.Context(), `SET LOCAL lock_timeout='1s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = access.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, w.tid); err != nil {
		t.Fatal("an access change waited for a stalled rules upload:", err)
	}
	if err = access.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	rest := fmt.Sprintf(`%d,"name":"Safety","rules":%s}`, s.Revision, jsonBytes([]Rule{lockedRule("safety", "Keep the floor.")}))
	if _, err = writer.Write([]byte(rest)); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	<-done
	if rec.Code != 200 {
		t.Fatalf("draft after a slow upload: %d %s", rec.Code, rec.Body.String())
	}
	// Oversized bodies are refused before any transaction.
	big := `{"expected_revision":1,"name":"` + strings.Repeat("a", int(maxBody)) + `"}`
	if code, _ := w.send(admin, "PUT", "/api/rules/sets/"+s.ID+"/draft", json.RawMessage(big)); code != 413 {
		t.Fatal("an oversized body was not refused:", code)
	}
}

// A rules write that cannot get the tenant row in time fails with 503 and
// changes nothing, instead of queueing indefinitely.
func TestRulesWriteGivesUpOnALockItCannotGet(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-timeout")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.layer(admin, Scope{Layer: "company"})
	s := w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor."))
	saved := lockTimeout
	lockTimeout = "300ms"
	t.Cleanup(func() { lockTimeout = saved })
	access, err := w.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer access.Rollback(t.Context())
	if _, err = access.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, w.tid); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	code, body := w.send(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")))
	if code != 503 || !strings.Contains(string(body), `"busy"`) || time.Since(started) > 5*time.Second {
		t.Fatalf("publish behind a held access lock: %d %s after %s", code, body, time.Since(started))
	}
	if w.published(s.ID) != "" {
		t.Fatal("a timed-out publication wrote")
	}
}

// Knowledge creation holds the tenant's foreign-key KEY SHARE and then needs
// the tenant advisory lock for its node. A rules publication holds that
// advisory lock and takes the tenant row: with FOR UPDATE the two would
// deadlock; with NO KEY UPDATE the publication proceeds and the insert follows.
func TestRulesPublishDoesNotDeadlockWithAForeignKeyReference(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-keyshare")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.layer(admin, Scope{Layer: "company"})
	s := w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor."))
	knowledge, err := w.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer knowledge.Rollback(t.Context())
	if _, err = knowledge.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR KEY SHARE`, w.tid); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		code, _ := w.send(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")))
		done <- code
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Fatal("publish next to a KEY SHARE holder:", code)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("publish waited for a foreign-key KEY SHARE holder")
	}
	// The knowledge transaction now takes the advisory lock its node insert needs.
	if _, err = knowledge.Exec(t.Context(), `SET LOCAL lock_timeout='2s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = knowledge.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, w.tid); err != nil {
		t.Fatal("the knowledge insert could not follow:", err)
	}
	if err = knowledge.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The demotion guard relies on access changes locking the tenant row FOR
// UPDATE, which conflicts with the NO KEY UPDATE taken by rules writes.
func TestAccessChangesStillLockTheTenantForUpdate(t *testing.T) {
	for _, file := range []string{"../authz/module.go", "../authz/project_members.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "FROM tenants WHERE id=$1::uuid FOR UPDATE") {
			t.Fatalf("%s no longer locks the tenant row FOR UPDATE; revisit rules lockAccess", file)
		}
	}
}

// customPublisher binds a workspace role with exactly these permissions.
func (w *batchWorld) customPublisher(name string, permissions ...string) (tenant.Principal, string) {
	w.t.Helper()
	var role string
	if err := w.d.Admin.QueryRow(w.t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,$2) RETURNING id::text`, w.tid, "custom_"+name).Scan(&role); err != nil {
		w.t.Fatal(err)
	}
	for _, permission := range permissions {
		if _, err := w.d.Admin.Exec(w.t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, w.tid, role, permission); err != nil {
			w.t.Fatal(err)
		}
	}
	return w.principal(tenant.Person, name, "custom_"+name), role
}

// A publisher who may not read the project's rules (nodes.read alone is not
// enough) learns no sizes: neither max_bytes nor a sized refusal.
func TestBatchBudgetSizesNeedRulesRead(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-rulesread")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	project := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	w.publish(admin, w.set(admin, project, "Aeon", bulky("project", 19)...), "260928090010.0.0")
	publisher, _ := w.customPublisher("publisher", "rules.publish", "rules.write", "nodes.read")
	// The admin prepares the drafts; the custom publisher publishes them.
	short := w.set(admin, company, "Short", testRule("short", "A short rule."))
	body := w.call(publisher, "POST", "/api/rules/publish", batch("", item(short, "auto")), 200)
	var ok BatchResult
	if err := json.Unmarshal(body, &ok); err != nil || ok.MaxBytes != 0 {
		t.Fatalf("sizes reached a publisher without rules.read: %s", body)
	}
	w.refusedHidden(publisher, "project over budget, unreadable", w.set(admin, company, "ThreeHalf", bulky("companyr", 8)...))
}

// A revocation of rules.read that commits while the publication waits for the
// access lock is honoured: the publication proceeds but reports no sizes.
func TestBatchBudgetHonoursARevocationWhileWaiting(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-revoke")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	project := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	w.publish(admin, w.set(admin, project, "Aeon", bulky("project", 19)...), "260928090011.0.0")
	publisher, role := w.customPublisher("publisher", "rules.publish", "rules.write", "rules.read", "nodes.read")
	short := w.set(admin, company, "Short", testRule("short", "A short rule."))
	access, err := w.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer access.Rollback(t.Context())
	if _, err = access.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, w.tid); err != nil {
		t.Fatal(err)
	}
	if _, err = access.Exec(t.Context(), `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='rules.read'`, w.tid, role); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		code int
		body []byte
	}
	done := make(chan answer, 1)
	go func() {
		code, body := w.send(publisher, "POST", "/api/rules/publish", batch("", item(short, "auto")))
		done <- answer{code, body}
	}()
	time.Sleep(300 * time.Millisecond)
	if err = access.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-done
	var ok BatchResult
	if got.code != 200 || json.Unmarshal(got.body, &ok) != nil || ok.MaxBytes != 0 {
		t.Fatalf("a revoked reader still learned sizes: %d %s", got.code, got.body)
	}
}

// bigStore is a store at the work bound: maxBudgetRules rules in sets of 50,
// all disabled (the slow case: every rule is walked, none is rendered).
func bigStore() []Snapshot {
	out := []Snapshot{floorSnapshot()}
	for i := range maxBudgetRules / 50 {
		rules := []Rule{}
		for j := range 50 {
			r := testRule(fmt.Sprintf("r%03d-%02d", i, j), "A rule of ordinary length that says what agents must do.")
			r.Enabled = false
			rules = append(rules, r)
		}
		out = append(out, testSnapshot(fmt.Sprintf("set-%03d", i), Scope{Layer: "company"}, rules...))
	}
	return sortSnapshots(out)
}

// The worst render the bounds allow, repeated up to the render cap, fits well
// inside the request deadline on this machine. A small sample measures the
// per-render cost here; the projection for the full cap must stay under a
// third of the deadline. No wall clock is asserted on the full run: on a runner
// so slow that even the projection passes the deadline, the test skips, since
// transaction_timeout is what guarantees the deadline in production.
func TestBudgetCapFitsTheDeadline(t *testing.T) {
	store := bigStore()
	c := testContext()
	c.AgentID = ""
	render := func() {
		if _, err := merge(c, store, time.Now(), true, nil, DefaultBudget()); err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Code != "floor_missing" && e.Code != "rules_budget_exceeded" {
				t.Fatal(err)
			}
		}
	}
	render() // warm up
	const sample = 50
	started := time.Now()
	for range sample {
		render()
	}
	perRender := time.Since(started) / sample
	projected := perRender * time.Duration(maxBudgetContexts)
	t.Logf("%s per render of a %d-rule store of disabled rules; %d renders project to %s (deadline %s)", perRender, maxBudgetRules, maxBudgetContexts, projected, txTimeout)
	if projected > txTimeout {
		t.Skipf("this machine renders too slowly to judge the cap (%s projected for %d renders, deadline %s); transaction_timeout still enforces the deadline", projected, maxBudgetContexts, txTimeout)
	}
	if projected > txTimeout/3 {
		t.Fatalf("%d renders project to %s, more than a third of the %s deadline; lower the caps", maxBudgetContexts, projected, txTimeout)
	}
}

// BenchmarkBudgetCheck measures the whole check as a publication runs it:
// loading, digest validation, authorization and rendering, over a store at the
// rules bound made of disabled rules.
func BenchmarkBudgetCheck(b *testing.B) {
	w := newBatchWorld(b, "rules-batch-bench")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	for i := range maxBudgetRules/50 - 1 {
		rules := bulky(fmt.Sprintf("b%03d", i), 50)
		for j := range rules {
			rules[j].Text = "A rule of ordinary length that says what agents must do."
			rules[j].Enabled = false
		}
		w.publish(admin, w.set(admin, company, fmt.Sprintf("Set %03d", i), rules...), time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Second).Format("060102150405")+".0.0")
	}
	b.ResetTimer()
	for b.Loop() {
		err := db.InTenant(tenant.WithPrincipal(b.Context(), admin), w.d.App, w.tid, func(tx pgx.Tx) error {
			if _, err := tx.Exec(b.Context(), `SELECT set_config('aeon.rules_access','on',true),set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_projects','*',true),set_config('aeon.visible_projects','*',true)`, admin.ID); err != nil {
				return err
			}
			sets, err := allSets(b.Context(), tx, company.ID)
			if err != nil {
				return err
			}
			ctx := withDeadline(b.Context(), time.Now().Add(time.Minute))
			_, err = budgetCheck(ctx, tx, admin, admin.ID, sets[:1], time.Now(), DefaultBudget())
			return err
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// An expired check stops even while every candidate is one the batch does not
// touch: enumeration is work too.
func TestBudgetEnumerationStopsAtTheDeadline(t *testing.T) {
	projects := []string{noProject}
	for i := range 10000 {
		projects = append(projects, fmt.Sprintf("10000000-0000-4000-8000-%012d", i))
	}
	nothing := []Set{{Scope: Scope{Layer: "person", OwnerID: testPerson}}}
	b := budget{work: &work{ctx: withDeadline(t.Context(), time.Now().Add(-time.Second))}, tenant: testTenant, batch: nothing, now: time.Now(), projects: projects, readable: map[string]bool{}}
	started := time.Now()
	if err := b.person(noPerson, nil); !errors.Is(err, errStopped) {
		t.Fatal("an expired check over 200,000 untouched candidates did not stop:", err)
	}
	if took := time.Since(started); took > 50*time.Millisecond {
		t.Fatal("stopping took", took)
	}
	if b.rendered != 0 {
		t.Fatal("rendered", b.rendered)
	}
}

// Past the store bound the publication is refused, never partially checked.
func TestBudgetCheckRefusesAnOversizedStore(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-storebound")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	w.publish(admin, w.set(admin, company, "Many", bulky("many", 5)...), "260928090020.0.0")
	saved := maxBudgetRules
	maxBudgetRules = 4
	t.Cleanup(func() { maxBudgetRules = saved })
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	body := w.call(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")), 422)
	if !strings.Contains(string(body), `"budget_check_too_large"`) || w.published(s.ID) != "" {
		t.Fatalf("an oversized store was not refused: %s", body)
	}
}

// lockTaken runs an access change that needs the tenant row, with a generous
// watchdog, and reports whether it got the row before the watchdog fired.
func (w *batchWorld) lockTaken() bool {
	w.t.Helper()
	access, err := w.d.Admin.Begin(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	defer access.Rollback(w.t.Context())
	if _, err = access.Exec(w.t.Context(), `SET LOCAL lock_timeout='10s'`); err != nil {
		w.t.Fatal(err)
	}
	_, err = access.Exec(w.t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, w.tid)
	return err == nil
}
func (w *batchWorld) nothingStored(label string, s Set) {
	w.t.Helper()
	var stored int
	if err := w.d.Admin.QueryRow(w.t.Context(), `SELECT count(*) FROM rule_publish_batches WHERE tenant_id=$1`, w.tid).Scan(&stored); err != nil || stored != 0 || w.published(s.ID) != "" {
		w.t.Fatalf("%s: a publication past its deadline committed (%d stored, %v)", label, stored, err)
	}
}

// Probe: a slow statement right before the answer is stored. While the
// statement still runs, the server ends the transaction at the deadline: the
// competing access change gets the row, the request answers 503, and nothing
// is stored.
func TestBatchNeverCommitsAfterItsDeadline(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-commit")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	savedTimeout, savedHook := txTimeout, beforeStore
	txTimeout = 600 * time.Millisecond
	entered := make(chan struct{})
	beforeStore = func(ctx context.Context, tx pgx.Tx) error {
		close(entered)
		_, err := tx.Exec(ctx, `SELECT pg_sleep(30)`)
		return err
	}
	t.Cleanup(func() { txTimeout, beforeStore = savedTimeout, savedHook })
	done := make(chan int, 1)
	go func() {
		code, _ := w.send(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")))
		done <- code
	}()
	<-entered
	if !w.lockTaken() {
		t.Fatal("the access change never got the row while the slow statement ran")
	}
	if code := <-done; code != 503 {
		t.Fatal("a publication past its deadline answered", code)
	}
	w.nothingStored("slow statement", s)
}

// Probe: one render held in the application (no statement running). The
// server still ends the transaction at the deadline, so the competing access
// change gets the row while the render is held; released, the request answers
// 503 and nothing is stored.
func TestBatchSlowRenderReleasesTheLockAtTheDeadline(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-slowrender")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	savedTimeout, savedHook := txTimeout, beforeRender
	txTimeout = 300 * time.Millisecond
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	beforeRender = func() {
		once.Do(func() { close(entered); <-release })
	}
	t.Cleanup(func() { txTimeout, beforeRender = savedTimeout, savedHook })
	done := make(chan int, 1)
	go func() {
		code, _ := w.send(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")))
		done <- code
	}()
	<-entered
	taken := w.lockTaken()
	close(release)
	if !taken {
		t.Fatal("the access change never got the row while the render was held")
	}
	if code := <-done; code != 503 {
		t.Fatal("a publication past its deadline answered", code)
	}
	w.nothingStored("held render", s)
}

// ackDropper loses the server's answer to exactly one COMMIT, after the server
// has processed it: the change is durable, the client sees EOF.
type ackDropper struct {
	armed atomic.Bool
	// hangUp loses the answer at once, without waiting for the server: the
	// COMMIT may still be running when the client gives up.
	hangUp atomic.Bool
}
type dropConn struct {
	net.Conn
	d    *ackDropper
	drop bool
}

func (c *dropConn) Write(b []byte) (int, error) {
	// With hangUp, the network is gone for the cancel request pgx sends after
	// the lost answer too (a CancelRequest packet is 16 bytes, code 80877102).
	if c.d.hangUp.Load() && len(b) == 16 && binary.BigEndian.Uint32(b[4:8]) == 80877102 {
		c.Conn.Close()
		return 0, io.ErrClosedPipe
	}
	if bytes.Contains(b, []byte("commit\x00")) && c.d.armed.CompareAndSwap(true, false) {
		c.drop = true
	}
	return c.Conn.Write(b)
}
func (c *dropConn) Read(b []byte) (int, error) {
	if c.drop {
		// Wait for the server's CommandComplete (the commit is durable by
		// then), throw it away and hang up; or hang up at once.
		if !c.d.hangUp.Load() {
			_ = c.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Conn.Read(make([]byte, 4096))
		}
		c.Conn.Close()
		return 0, io.EOF
	}
	return c.Conn.Read(b)
}

// faultyWorld serves the rules API over a pool whose connections can lose one
// COMMIT acknowledgement.
func faultyWorld(t *testing.T, slug string) (*batchWorld, *ackDropper) {
	w := newBatchWorld(t, slug)
	cfg, err := pgxpool.ParseConfig(w.d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.TLSConfig = nil
	cfg.ConnConfig.Fallbacks = nil
	cfg.ConnConfig.RuntimeParams["jit"] = "off"
	dropper := &ackDropper{}
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &dropConn{Conn: c, d: dropper}, nil
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	w.mux = http.NewServeMux()
	New(pool).Mount(w.mux)
	return w, dropper
}

// unknownAnswer asserts an outcome_unknown answer with this wording.
func unknownAnswer(t *testing.T, label string, body []byte, wording string) {
	t.Helper()
	var e map[string]any
	if err := json.Unmarshal(body, &e); err != nil || e["code"] != "outcome_unknown" || e["error"] != wording {
		t.Fatalf("%s: %s", label, body)
	}
	if wording != unknownBatch && strings.Contains(strings.ToLower(string(body)), "safe") {
		t.Fatalf("%s: promises a safe retry: %s", label, body)
	}
}

// The COMMIT succeeds on the server but its acknowledgement is lost, and the
// caller's access is revoked before the check. The batch's stored answer is
// read without depending on that access: 200 with the stored result, exactly
// as a replay would give.
func TestBatchLostCommitAcknowledgementReturnsTheStoredResult(t *testing.T) {
	w, dropper := faultyWorld(t, "rules-batch-lostack")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	req := batch("Lost ack.", item(s, "auto"))
	saved := reconcileHook
	reconcileHook = func() error {
		_, err := w.d.Admin.Exec(context.Background(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, w.tid, admin.ID)
		return err
	}
	t.Cleanup(func() { reconcileHook = saved })
	dropper.armed.Store(true)
	first := w.call(admin, "POST", "/api/rules/publish", req, 200)
	if dropper.armed.Load() {
		t.Fatal("no COMMIT acknowledgement was lost; the test did not exercise the fault")
	}
	var got BatchResult
	if err := json.Unmarshal(first, &got); err != nil || w.published(s.ID) != got.Versions[0].Version {
		t.Fatalf("reconciled answer: %s", first)
	}
	var stored []byte
	if err := w.d.Admin.QueryRow(t.Context(), `SELECT result::text FROM rule_publish_batches WHERE tenant_id=$1 AND batch_id=$2`, w.tid, got.BatchID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var fromStore BatchResult
	if err := json.Unmarshal(stored, &fromStore); err != nil || !bytes.Equal(jsonBytes(fromStore), jsonBytes(got)) {
		t.Fatalf("the answer is not the stored one\nanswer %s\nstored %s", first, jsonBytes(fromStore))
	}
}

// A COMMIT still running (held in a deferred trigger) when the client loses
// the connection: the stored answer is not there yet, and absence is not a
// rollback. The answer is outcome_unknown, and the publication does land.
func TestBatchCommitStillRunningIsUnknownNotBusy(t *testing.T) {
	w, dropper := faultyWorld(t, "rules-batch-inflight")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	for _, stmt := range []string{
		`CREATE FUNCTION aeon_test_slow_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1); RETURN NULL; END $$`,
		`CREATE CONSTRAINT TRIGGER aeon_test_slow_commit AFTER INSERT ON rule_publish_batches DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION aeon_test_slow_commit()`,
	} {
		if _, err := w.d.Admin.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	dropper.hangUp.Store(true)
	dropper.armed.Store(true)
	body := w.call(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")), 503)
	unknownAnswer(t, "commit in flight", body, unknownBatch)
	deadline := time.Now().Add(10 * time.Second)
	for w.published(s.ID) == "" {
		if time.Now().After(deadline) {
			t.Fatal("the publication never landed; the test did not exercise an in-flight commit")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Every other write cannot find out what happened: it says so, in its own
// words, and never promises that repeating is safe.
func TestLostAcknowledgementOfOtherWritesIsUnknown(t *testing.T) {
	w, dropper := faultyWorld(t, "rules-other-lostack")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	lose := func(method, path string, in any, wording, label string) {
		t.Helper()
		dropper.armed.Store(true)
		code, body := w.send(admin, method, path, in)
		if dropper.armed.Load() || code != 503 {
			t.Fatalf("%s: %d %s", label, code, body)
		}
		unknownAnswer(t, label, body, wording)
	}
	lose("PUT", "/api/rules/sets/"+s.ID+"/draft", draftInput{ExpectedRevision: s.Revision, Name: "Short", Rules: []Rule{testRule("short", "A shorter rule.")}}, unknownChange, "draft")
	lose("POST", "/api/rules/sets/"+s.ID+"/publish", map[string]any{"expected_revision": s.Revision + 1, "version": "261001000000.0.0"}, unknownChange, "single publish")
	lose("POST", "/api/rules/sets/"+s.ID+"/restore", map[string]any{"expected_revision": s.Revision + 1, "version": "261001000000.0.0", "new_version": "261001000001.0.0"}, unknownChange, "restore")
	lose("POST", "/api/rules/sets", map[string]any{"layer_id": company.ID, "name": "Maybe"}, unknownSet, "set creation")
	lose("POST", "/api/rules/layers", Scope{Layer: "person", OwnerID: admin.ID}, unknownLayer, "layer creation")
	// The wording is truthful: the set was in fact created.
	var n int
	if err := w.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND rule_resource='set' AND title='Maybe'`, w.tid).Scan(&n); err != nil || n != 1 {
		t.Fatal("the set whose creation went unacknowledged:", n, err)
	}
}

// When even the check fails, a batch publication's outcome is unknown, never
// "nothing was changed".
func TestBatchLostAcknowledgementWithoutReconciliationIsUnknown(t *testing.T) {
	w, dropper := faultyWorld(t, "rules-batch-unknown")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.floor(admin)
	s := w.set(admin, company, "Short", testRule("short", "A short rule."))
	saved := reconcileHook
	reconcileHook = func() error { return errors.New("reconciliation unavailable") }
	t.Cleanup(func() { reconcileHook = saved })
	dropper.armed.Store(true)
	body := w.call(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")), 503)
	unknownAnswer(t, "failed check", body, unknownBatch)
	if w.published(s.ID) == "" {
		t.Fatal("the fault did not let the commit through; the test did not exercise the case")
	}
}
