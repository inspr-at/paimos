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
	t       *testing.T
	d       *dbtest.DB
	tid     string
	mux     *http.ServeMux
	project string
}

func newBatchWorld(t *testing.T, slug string) *batchWorld {
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
	if err := json.Unmarshal(w.call(p, "PUT", "/api/rules/sets/"+s.ID+"/draft", draftInput{1, name, rules}, 200), &s); err != nil {
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

// The byte budget covers every context a publication changes, including ones
// the publishing person cannot see.
func TestBatchBudgetCoversEveryAffectedContext(t *testing.T) {
	w := newBatchWorld(t, "rules-batch-budget")
	admin := w.principal(tenant.Person, "owner", "admin")
	company := w.layer(admin, Scope{Layer: "company"})
	w.publish(admin, w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor.")), "260928090000.0.0")
	refused := func(label string, s Set) {
		t.Helper()
		body := w.call(admin, "POST", "/api/rules/publish", batch("", item(s, "auto")), 422)
		var e Error
		if err := json.Unmarshal(body, &e); err != nil || e.Code != "rules_budget_exceeded" || e.ActualBytes <= MaxBytes {
			t.Fatalf("%s: %s", label, body)
		}
		if strings.Contains(string(body), "Explain every step") || strings.Contains(string(body), "person-") || strings.Contains(string(body), "agent-") {
			t.Fatalf("%s: the refusal leaks rule content: %s", label, body)
		}
		if v := w.published(s.ID); v != "" {
			t.Fatalf("%s: published anyway as %s", label, v)
		}
	}

	// (a) Aeon's project rules switch the big role rules off, but a project
	// without project rules would receive all of them.
	project := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	off := bulky("role", 28)
	for i := range off {
		off[i].Enabled = false
	}
	w.publish(admin, w.set(admin, project, "Quiet", off...), "260928090001.0.0")
	role := w.layer(admin, Scope{Layer: "agent", Role: "builder"})
	loud := w.set(admin, role, "Loud", bulky("role", 28)...)
	refused("project without project rules", loud)

	// (b) An existing named-agent contribution counts although its set is not
	// in the batch: 7 KB of agent rules plus 6 KB of new company rules.
	agent := w.principal(tenant.Agent, "worker", "member")
	if _, err := w.d.Admin.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'rules-fixture','rules-fixture-batch','fixture-not-a-credential',$3)`, w.tid, agent.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	named := w.layer(admin, Scope{Layer: "agent", OwnerID: admin.ID, AgentID: agent.ID})
	w.publish(admin, w.set(admin, named, "Persona", bulky("agent", 15)...), "260928090002.0.0")
	sixKB := w.set(admin, company, "Six", bulky("company", 13)...)
	refused("existing named agent", sixKB)

	// (c) Another owner's private person rules count too, although the
	// publishing person cannot read them.
	other := w.principal(tenant.Person, "colleague", "admin")
	private := w.layer(other, Scope{Layer: "person", OwnerID: other.ID})
	w.publish(other, w.set(other, private, "Mine", bulky("person", 17)...), "260928090003.0.0")
	fourKB := w.set(admin, company, "Four", bulky("company4", 9)...)
	// Without the colleague's 8 KB the four new company KB would fit next to
	// the admin's own contexts (floor + 4 KB), but the colleague's file would not.
	refused("another owner's private rules", fourKB)
	// The admin's own visibility is restored after the check.
	if code, body := w.send(admin, "GET", "/api/rules/sets/"+fourKB.ID, nil); code != 200 {
		t.Fatalf("visibility not restored: %d %s", code, body)
	}
	small := w.set(admin, company, "Small", testRule("small", "A short rule."))
	var ok BatchResult
	if err := json.Unmarshal(w.call(admin, "POST", "/api/rules/publish", batch("", item(small, "auto")), 200), &ok); err != nil || ok.MaxBytes <= 0 || ok.MaxBytes > MaxBytes {
		t.Fatalf("a small publication failed the budget: %+v %v", ok, err)
	}
}
