// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func uid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type fixture struct {
	d                                                                            *dbtest.DB
	m                                                                            *Module
	mux                                                                          *http.ServeMux
	keys                                                                         http.Handler
	person, agent, otherAgent, foreign                                           tenant.Principal
	project, hidden, ticket, hiddenTicket, foreignProject, session, otherSession string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t)}
	ctx := t.Context()
	newTenant := func(slug string) string {
		var id string
		if err := f.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ta, tb := newTenant("questions-a"), newTenant("questions-b")
	person := func(tid string, kind tenant.PrincipalKind, name string) tenant.Principal {
		p := tenant.Principal{TenantID: tid, Kind: kind, Name: name}
		if err := f.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, string(kind), name).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.person = person(ta, tenant.Person, "owner")
	f.agent = person(ta, tenant.Agent, "worker")
	f.otherAgent = person(ta, tenant.Agent, "other")
	f.foreign = person(tb, tenant.Person, "foreign")
	dbtest.BindRole(t, f.d, ta, f.person.ID, "owner")
	dbtest.BindRole(t, f.d, tb, f.foreign.ID, "owner")
	node := func(tid, kind, parent string) string {
		var id string
		err := db.InTenant(dbtest.Seed(ctx), f.d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Fixture',$3 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug=$2 RETURNING id::text`, tid, kind, nullable(parent)).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.project = node(ta, "project", "")
	f.hidden = node(ta, "project", "")
	f.ticket = node(ta, "ticket", f.project)
	f.hiddenTicket = node(ta, "ticket", f.hidden)
	f.foreignProject = node(tb, "project", "")
	for _, p := range []tenant.Principal{f.agent, f.otherAgent} {
		if _, err := f.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, ta, p.ID, f.project); err != nil {
			t.Fatal(err)
		}
	}
	f.agent.Scopes = []string{"questions.ask", "questions.read", "nodes.read", "nodes.write", "knowledge.write"}
	f.otherAgent.Scopes = f.agent.Scopes
	session := func(p tenant.Principal) string {
		var id string
		err := f.d.Admin.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','test','unmanaged','worker','unknown',$4,$5) RETURNING id::text`, ta, f.project, p.ID, []byte(uid()), []byte(uid())).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.session = session(f.agent)
	f.otherSession = session(f.otherAgent)
	f.m = New(f.d.App)
	f.mux = http.NewServeMux()
	f.m.Mount(f.mux)
	nodes.New(f.d.App, nodes.SQLWriter{}).Mount(f.mux)
	knowledge.New(f.d.App).Mount(f.mux)
	am, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)}, f.d.App)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httpapi.Server{Pool: f.d.App, Modules: []httpapi.Module{f.m}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	f.keys = srv.Handler()
	return f
}
func input() Input {
	return Input{RequestID: uid(), Question: "Which storage?", Context: "Private context", Findings: "Both fit.", Options: []Option{{ID: "a", Title: "Local", Description: "Simple", Answer: "Use local storage."}, {ID: "b", Title: "Remote", Description: "Shared", Answer: "Use remote storage."}}, Recommend: "a", Why: "No sharing needed", Meanwhile: "carries_on"}
}
func request(ctx context.Context, h http.Handler, p tenant.Principal, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	if raw, ok := body.(string); ok {
		b = []byte(raw)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(ctx, p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func question(t *testing.T, w *httptest.ResponseRecorder, status int) Question {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
	}
	var q Question
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	return q
}
func (f *fixture) ask(t *testing.T, in Input) Question {
	t.Helper()
	return question(t, request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", in), 201)
}

func TestQuestionLifecycleAndIsolation(t *testing.T) {
	f := newFixture(t)
	in := input()
	in.TicketID = f.ticket
	in.SessionID = f.session
	q := f.ask(t, in)
	if q.Revision != 1 || q.State != "open" || q.SuggestedOutcome != "once" || len(q.Askers) != 1 || q.Askers[0].PrincipalID != f.agent.ID || q.Askers[0].CommentNodeID != f.ticket || q.Askers[0].SessionID != f.session || q.Askers[0].ReplyRootID == "" {
		t.Fatalf("incorrect durable question: %+v", q)
	}
	again := question(t, request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", in), 200)
	if again.ID != q.ID || again.Askers[0].ReplyRootID != q.Askers[0].ReplyRootID {
		t.Fatal("replay changed identity")
	}
	changed := in
	changed.Context = "changed"
	if w := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", changed); w.Code != 409 {
		t.Fatalf("replay conflict=%d", w.Code)
	}
	ticketless := input()
	ticketless.AnywayReason = "Different constraints"
	q2 := f.ask(t, ticketless)
	if q2.SuggestedOutcome != "always" || q2.Askers[0].CommentNodeID != q2.ID || q2.Input.AnywayReason != ticketless.AnywayReason {
		t.Fatal("ticketless default/destination/reason")
	}
	override := input()
	override.TicketID = f.ticket
	override.SuggestedOutcome = "doctrine"
	if f.ask(t, override).SuggestionReason != "agent_suggestion" {
		t.Fatal("suggestion lost")
	}
	for _, p := range []tenant.Principal{f.otherAgent, f.foreign} {
		if w := request(t.Context(), f.mux, p, "GET", "/api/questions/"+q.ID, nil); w.Code != 404 {
			t.Fatalf("foreign reader status %d", w.Code)
		}
	}
	for _, target := range []struct{ project, ticket, session string }{{f.hidden, "", ""}, {f.foreignProject, "", ""}, {f.project, f.hiddenTicket, ""}, {f.project, "", f.otherSession}} {
		x := input()
		x.TicketID = target.ticket
		x.SessionID = target.session
		if w := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+target.project+"/questions", x); w.Code != 404 {
			t.Fatalf("foreign reference status %d: %s", w.Code, w.Body.String())
		}
	}
	blocked := input()
	blocked.BlockedNodeIDs = []string{f.hiddenTicket}
	if w := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", blocked); w.Code != 404 {
		t.Fatalf("foreign blocked link: %d", w.Code)
	}
	source := input()
	source.SourceRequestID = uid()
	if w := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", source); w.Code != 404 {
		t.Fatalf("forged source: %d", w.Code)
	}
	// The scoped key sees no other asker's question even within its own project.
	var page Page
	w := request(t.Context(), f.mux, f.otherAgent, "GET", "/api/decision-desk", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Items) != 0 {
		t.Fatal("another asker's desk leaked")
	}
	// A project-specific permission cannot authorize the same principal's other project.
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, f.agent.TenantID, f.agent.ID, f.hidden); err != nil {
		t.Fatal(err)
	}
	hiddenIn := input()
	hidden := question(t, request(t.Context(), f.mux, f.person, "POST", "/api/projects/"+f.hidden+"/questions", hiddenIn), 201)
	if w := request(t.Context(), f.mux, f.agent, "GET", "/api/questions/"+hidden.ID, nil); w.Code != 404 {
		t.Fatal("non-asker can read hidden question")
	}
	// Session closure does not erase the original route or invalidate a retry.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, f.agent.TenantID, f.session); err != nil {
		t.Fatal(err)
	}
	question(t, request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", in), 200)
	fresh := in
	fresh.RequestID = uid()
	if w := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", fresh); w.Code != 409 {
		t.Fatal("new ask on ended session accepted")
	}
	question(t, request(t.Context(), f.mux, f.agent, "GET", "/api/questions/"+q.ID+"/status", nil), 200)
	// Tenant RLS and project RLS apply under the actual non-bypass app role.
	for _, p := range []tenant.Principal{f.foreign} {
		err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM desk_questions`).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatal("RLS exposed foreign question")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestQuestionHumanDecisionAndGenericForgery(t *testing.T) {
	f := newFixture(t)
	q := f.ask(t, input())
	d := DecisionInput{RequestID: uid(), ExpectedRevision: 1, OptionID: "a", Outcome: "once"}
	for _, p := range []tenant.Principal{f.agent, f.otherAgent} {
		p.Scopes = append(p.Scopes, "questions.decide")
		if w := request(t.Context(), f.mux, p, "POST", "/api/questions/"+q.ID+"/decision", d); w.Code != 403 {
			t.Fatal("agent decided")
		}
	}
	for _, stamp := range []string{"always", "requirement", "doctrine"} {
		x := d
		x.Outcome = stamp
		if w := request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", x); w.Code != 422 {
			t.Fatal("unsupported outcome published")
		}
	}
	decided := question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", d), 200)
	if decided.Revision != 2 || decided.Answer == nil || decided.Answer.Answer != q.Input.Options[0].Answer || decided.Answer.DecidedBy != f.person.ID || len(decided.Pending) != 3 {
		t.Fatalf("wrong answer projection: %+v", decided)
	}
	if delta := decided.Answer.DeliverAfter.Sub(decided.Answer.CreatedAt); delta < 9*time.Second || delta > 11*time.Second {
		t.Fatal("wrong grace deadline")
	}
	for _, e := range decided.Pending {
		if e.State != "pending" {
			t.Fatal("claimed delivery before dispatcher")
		}
	}
	question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", d), 200)
	bad := d
	bad.Answer = "conflicting replay"
	if w := request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", bad); w.Code != 409 {
		t.Fatal("conflicting decision replay")
	}
	bad = d
	bad.RequestID = uid()
	if w := request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", bad); w.Code != 409 {
		t.Fatal("stale decision")
	}
	d.RequestID = uid()
	d.ExpectedRevision = 2
	d.OptionID = "b"
	updated := question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", d), 200)
	if updated.Revision != 3 || updated.Answer.ID == decided.Answer.ID || !updated.Answer.DeliverAfter.After(decided.Answer.DeliverAfter) {
		t.Fatal("revision/deadline not advanced")
	}
	var oldPending, oldAnswers int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM desk_pending WHERE question_id=$1 AND revision=2 AND state='replaced'`, q.ID).Scan(&oldPending); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM desk_answers WHERE question_id=$1`, q.ID).Scan(&oldAnswers); err != nil {
		t.Fatal(err)
	}
	if oldPending != 3 || oldAnswers != 2 {
		t.Fatal("history erased or effects unreplaced")
	}
	// Generic node paths and knowledge type cannot create/alter human authority.
	for _, id := range []string{q.ID, updated.Answer.ID} {
		for _, verb := range []string{"PATCH", "DELETE"} {
			w := request(t.Context(), f.mux, f.person, verb, "/api/nodes/"+id, map[string]any{"fields": map[string]any{"decided_by": f.agent.ID, "state": "active"}})
			if w.Code < 400 {
				t.Fatalf("generic %s accepted", verb)
			}
		}
	}
	var kind string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT kind_id::text FROM nodes WHERE id=$1`, q.ID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		path string
		body any
	}{{"/api/nodes", map[string]any{"kind_id": kind, "parent_id": f.project, "title": "Forged", "fields": map[string]any{"decided_by": f.person.ID}}}, {"/api/knowledge", map[string]any{"project_id": f.project, "type": "decision", "title": "Forged", "slug": "forged", "status": "active"}}} {
		if w := request(t.Context(), f.mux, f.person, "POST", entry.path, entry.body); w.Code < 400 {
			t.Fatal("generic forge accepted")
		}
	}
	if w := request(t.Context(), f.mux, f.person, "PATCH", "/api/kinds/"+kind, map[string]any{"slug": "task"}); w.Code < 400 {
		t.Fatal("reserved kind renamed")
	}
	// Even direct generic SQL under the app role cannot bypass service ownership.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE desk_decisions SET state='active' WHERE question_id=$1`, q.ID)
		return err
	})
	if err == nil {
		t.Fatal("direct projection forgery accepted")
	}
	var payload string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT title||body||fields::text FROM nodes WHERE id=$1`, q.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, q.Input.Context) || strings.Contains(payload, q.Input.Question) {
		t.Fatal("private question copied into generic node")
	}
}

func TestQuestionInputBoundsAndAttribution(t *testing.T) {
	f := newFixture(t)
	path := "/api/projects/" + f.project + "/questions"
	for _, field := range []string{"tenant_id", "principal_id", "askers", "decided_by", "delivery_state", "revision", "state"} {
		raw, _ := json.Marshal(input())
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		body[field] = f.person.ID
		if w := request(t.Context(), f.mux, f.agent, "POST", path, body); w.Code != 400 {
			t.Fatalf("forged %s status=%d", field, w.Code)
		}
	}
	for _, mutate := range []func(*Input){func(i *Input) { i.Question = "" }, func(i *Input) { i.Question = strings.Repeat("x", 2001) }, func(i *Input) { i.Context = strings.Repeat("ü", 8001) }, func(i *Input) { i.Options = append(i.Options, i.Options[0]) }, func(i *Input) { i.Options = make([]Option, 10) }, func(i *Input) { i.Recommend = "missing" }, func(i *Input) { i.Meanwhile = "invented" }, func(i *Input) { i.Question = "bad\x00value" }, func(i *Input) { i.AnywayReason = "   " }} {
		in := input()
		mutate(&in)
		if w := request(t.Context(), f.mux, f.agent, "POST", path, in); w.Code != 400 {
			t.Fatalf("invalid input status=%d", w.Code)
		}
	}
	if w := request(t.Context(), f.mux, f.agent, "POST", path, `{"context":"`+strings.Repeat("x", MaxBody)+`"}`); w.Code != 413 {
		t.Fatalf("oversized body=%d", w.Code)
	}
	for _, raw := range []string{"null", "{} {}", "[]"} {
		if w := request(t.Context(), f.mux, f.agent, "POST", path, raw); w.Code != 400 {
			t.Fatalf("invalid body %s=%d", raw, w.Code)
		}
	}
	if w := request(t.Context(), f.mux, tenant.Principal{}, "POST", path, input()); w.Code != 401 {
		t.Fatal("anonymous ask")
	}
	p := f.agent
	p.Scopes = []string{"questions.ask"}
	if w := request(t.Context(), f.mux, p, "POST", path, input()); w.Code != 404 {
		t.Fatal("ask without read scope")
	}
}

func TestQuestionConcurrentReplayAndAnswer(t *testing.T) {
	f := newFixture(t)
	in := input()
	path := "/api/projects/" + f.project + "/questions"
	start := make(chan struct{})
	got := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; got <- request(t.Context(), f.mux, f.agent, "POST", path, in) }()
	}
	close(start)
	wg.Wait()
	close(got)
	var id string
	codes := map[int]int{}
	for w := range got {
		codes[w.Code]++
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("concurrent ask=%d %s", w.Code, w.Body.String())
		}
		q := question(t, w, w.Code)
		if id != "" && q.ID != id {
			t.Fatal("duplicate questions")
		}
		id = q.ID
	}
	if codes[200] != 1 || codes[201] != 1 {
		t.Fatalf("replay status counts %v", codes)
	}
	start = make(chan struct{})
	got = make(chan *httptest.ResponseRecorder, 2)
	for _, choice := range []string{"a", "b"} {
		wg.Add(1)
		go func(choice string) {
			defer wg.Done()
			<-start
			got <- request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+id+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: 1, OptionID: choice, Outcome: "once"})
		}(choice)
	}
	close(start)
	wg.Wait()
	close(got)
	codes = map[int]int{}
	for w := range got {
		codes[w.Code]++
	}
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatalf("concurrent answers %v", codes)
	}
}

func (f *fixture) key(t *testing.T, scopes []string, revoked, expired bool) string {
	t.Helper()
	secret := uid()
	hash := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + strings.ReplaceAll(uid(), "-", "")[:16]
	var expires, revoke *time.Time
	old := time.Now().Add(-time.Hour)
	if expired {
		expires = &old
	}
	if revoked {
		revoke = &old
	}
	_, err := f.d.Admin.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,revoked_at,expires_at,created_by_principal_id) VALUES($1,$2,'question test',$3,$4,$5,$6,$7,$8)`, f.agent.TenantID, f.agent.ID, prefix, hex.EncodeToString(hash[:]), scopes, revoke, expires, f.person.ID)
	if err != nil {
		t.Fatal(err)
	}
	return "aeon_" + prefix + "_" + secret
}
func TestQuestionRealAgentKeyBoundary(t *testing.T) {
	f := newFixture(t)
	path := "/api/projects/" + f.project + "/questions"
	q := f.ask(t, input())
	for _, c := range []struct {
		name             string
		scopes           []string
		revoked, expired bool
		method, path     string
		status           int
	}{
		{"scoped", f.agent.Scopes, false, false, "POST", path, 201},
		{"narrow", []string{"nodes.read"}, false, false, "POST", path, 403},
		{"revoked", f.agent.Scopes, true, false, "POST", path, 401},
		{"expired", f.agent.Scopes, false, true, "POST", path, 401},
		{"cannot decide", []string{"questions.decide", "questions.read"}, false, false, "POST", "/api/questions/" + q.ID + "/decision", 403},
		{"status", f.agent.Scopes, false, false, "GET", "/api/questions/" + q.ID + "/status", 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			key := f.key(t, c.scopes, c.revoked, c.expired)
			body, _ := json.Marshal(input())
			r := httptest.NewRequest(c.method, c.path, bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			f.keys.ServeHTTP(w, r)
			if w.Code != c.status {
				t.Fatalf("HTTP %d want %d: %s", w.Code, c.status, w.Body.String())
			}
		})
	}
	// Creator's live project ceiling is re-evaluated, not copied into the ask.
	key := f.key(t, f.agent.Scopes, false, false)
	var backupOwner string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','backup owner') RETURNING id::text`, f.person.TenantID).Scan(&backupOwner); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, f.person.TenantID, backupOwner, "owner")
	if _, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.person.TenantID, f.person.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(input())
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	f.keys.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("revoked creator ceiling: %d", w.Code)
	}
	if perm, _ := authz.Lookup("questions.decide"); perm.AgentGrantable {
		t.Fatal("decision authority is key-grantable")
	}
}
