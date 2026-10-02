// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	d                             *dbtest.DB
	m                             *Module
	person, reader, agent         tenant.Principal
	project, otherProject, ticket string
	mux                           *http.ServeMux
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), mux: http.NewServeMux()}
	ctx := t.Context()
	var tid string
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('desk','Desk') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	principal := func(kind tenant.PrincipalKind) tenant.Principal {
		p := tenant.Principal{TenantID: tid, Kind: kind}
		if err := f.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$2) RETURNING id::text`, tid, kind).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.person, f.reader, f.agent = principal(tenant.Person), principal(tenant.Person), principal(tenant.Agent)
	dbtest.BindRole(t, f.d, tid, f.person.ID, "owner")
	dbtest.BindRole(t, f.d, tid, f.agent.ID, "member")
	f.agent.Scopes = []string{"questions.ask", "questions.read", "inbox.send"}
	node := func(kind, parent string) string {
		var id string
		err := db.InTenant(dbtest.Seed(ctx), f.d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Work',nullif($3,'')::uuid FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug=$2 RETURNING id::text`, tid, kind, parent).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.project, f.otherProject = node("project", ""), node("project", "")
	f.ticket = node("ticket", f.project)
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, f.reader.ID, f.project); err != nil {
		t.Fatal(err)
	}
	f.m = New(f.d.App)
	f.m.Mount(f.mux)
	questions.New(f.d.App).Mount(f.mux)
	messaging, err := inbox.NewMessaging(f.d.App, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	messaging.Mount(f.mux)
	return f
}

func (f *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.d.Admin.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) ask(t *testing.T, project, meanwhile string, blocked []string) questions.Question {
	t.Helper()
	var requestID string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT gen_random_uuid()::text`).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	in := questions.Input{RequestID: requestID, Question: "Private question " + requestID, Meanwhile: meanwhile, BlockedNodeIDs: blocked, Options: []questions.Option{{ID: "yes", Title: "Yes", Answer: "Proceed"}}}
	return f.askInput(t, project, in)
}

func (f *fixture) askInput(t *testing.T, project string, in questions.Input) questions.Question {
	t.Helper()
	b, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/api/projects/"+project+"/questions", bytes.NewReader(b))
	r = r.WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("ask %d: %s", w.Code, w.Body.String())
	}
	var q questions.Question
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func (f *fixture) decide(t *testing.T, q questions.Question, answer string) questions.Question {
	t.Helper()
	var requestID string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT gen_random_uuid()::text`).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	in := questions.DecisionInput{RequestID: requestID, ExpectedRevision: q.Revision, OptionID: "yes", Answer: answer, Outcome: "once"}
	b, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/api/questions/"+q.ID+"/decision", bytes.NewReader(b)).WithContext(tenant.WithPrincipal(t.Context(), f.person))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("decision %d: %s", w.Code, w.Body.String())
	}
	var got questions.Question
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *fixture) approval(t *testing.T, resource string, expiry time.Time, run ...string) string {
	t.Helper()
	var id string
	kind := "node"
	if resource == "" {
		kind = "tenant"
	}
	var runID any
	if len(run) > 0 {
		runID = run[0]
	}
	err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at,proposed_at,run_id) VALUES($1,$2,$2,'nodes.write',$3,nullif($4,'')::uuid,'Private rationale',$5,least(now(),$5::timestamptz-interval '1 hour'),$6) RETURNING id::text`, f.person.TenantID, f.agent.ID, kind, resource, expiry, runID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) page(t *testing.T, p tenant.Principal, limit int, after *cursor) Page {
	t.Helper()
	page, err := f.m.Read(t.Context(), p, limit, after)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestMixedOrderCountsFanInAndKeyset(t *testing.T) {
	f := setup(t)
	normal := f.ask(t, f.project, "carries_on", []string{f.ticket})
	labelOnly := f.ask(t, f.project, "parked", nil)
	held := f.ask(t, f.project, "paused", []string{f.ticket})
	later := f.approval(t, f.ticket, time.Now().Add(2*time.Hour))
	first := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	tenantApproval := f.approval(t, "", time.Now().Add(3*time.Hour))
	// Seed P2's fan-in boundary using two different agents and preserved input.
	// P2 is a separate package; this projection must still count its output once.
	var secondAgent string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','second') RETURNING id::text`, f.person.TenantID).Scan(&secondAgent); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, f.person.TenantID, secondAgent, "member")
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,comment_node_id,input) SELECT tenant_id,project_id,question_id,$2,request_id,request_digest,comment_node_id,input FROM desk_askers WHERE question_id=$1`, held.ID, secondAgent)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE desk_questions SET created_at=$1 WHERE node_id=ANY($2::uuid[])`, at, []string{normal.ID, labelOnly.ID, held.ID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	page := f.page(t, f.person, 2, nil)
	if page.Counts.Open != 6 || page.Counts.Held != 3 || !page.HasMore || page.Items[0].ID != first || page.Items[1].ID != later {
		t.Fatalf("mixed first page: %+v", page)
	}
	all := append([]Item{}, page.Items...)
	for page.HasMore {
		c, err := decodeCursor(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		page = f.page(t, f.person, 2, c)
		all = append(all, page.Items...)
	}
	quiet := []string{normal.ID, labelOnly.ID}
	sort.Strings(quiet)
	want := []string{first, later, held.ID, quiet[0], quiet[1], tenantApproval}
	for i, item := range all {
		if item.ID != want[i] {
			t.Fatalf("order %d: %s want %s", i, item.ID, want[i])
		}
		if item.ID == normal.ID && item.PushEligible(page.AsOf) {
			t.Fatal("carries_on caused push")
		}
		if item.ID == labelOnly.ID && item.PushEligible(page.AsOf) {
			t.Fatal("parked label without actual work caused push")
		}
	}
	if len(all) != 6 {
		t.Fatalf("fan-in duplicated source: %d", len(all))
	}
}

func TestRevocationDecisionExpiryAndFinishedWork(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "stopped", []string{f.ticket})
	f.ask(t, f.otherProject, "carries_on", nil)
	approval := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	page := f.page(t, f.reader, 100, nil)
	if page.Counts.Open != 2 || page.Counts.Held != 2 {
		t.Fatalf("project access: %+v", page)
	}
	f.exec(t, `UPDATE nodes SET state='done' WHERE id=$1`, f.ticket)
	page = f.page(t, f.reader, 100, nil)
	if page.Counts.Held != 0 {
		t.Fatal("finished work remains held")
	}
	f.exec(t, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'denied')`, f.person.TenantID, approval, f.person.ID)
	f.approval(t, f.ticket, time.Now().Add(-time.Hour))
	// A real answer and its grace edit must remove the source immediately,
	// while retaining the revision and replaced delivery evidence.
	answered := f.decide(t, q, "Proceed")
	edited := f.decide(t, answered, "Proceed with correction")
	if edited.Revision != q.Revision+2 || edited.Answer == nil || len(edited.Pending) == 0 {
		t.Fatal("grace fixture lost its answer/delivery evidence")
	}
	var answers, replaced int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM desk_answers WHERE question_id=$1`, q.ID).Scan(&answers); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM desk_pending WHERE question_id=$1 AND state='replaced'`, q.ID).Scan(&replaced); err != nil {
		t.Fatal(err)
	}
	if answers != 2 || replaced == 0 {
		t.Fatal("grace edit fixture did not retain history")
	}
	if got := f.page(t, f.reader, 100, nil); got.Counts.Open != 0 {
		t.Fatalf("expired/answered item survives: %+v", got)
	}
	f.exec(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.reader.ID)
	if got := f.page(t, f.reader, 100, nil); got.Counts.Open != 0 || len(got.Items) != 0 {
		t.Fatal("revoked reader received counts or links")
	}
	// A forged current role list cannot revive revoked authority.
	f.reader.Roles = []string{"owner"}
	if got := f.page(t, f.reader, 100, nil); got.Counts.Open != 0 {
		t.Fatal("trusted stale role hint")
	}
}

func TestPushPolicyAndHTTPBounds(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	expiry := now.Add(NearExpiry)
	for _, tc := range []struct {
		i    Item
		want bool
	}{
		{Item{Kind: "approval", ExpiresAt: &expiry}, true},
		{Item{Kind: "approval", Held: true}, true},
		{Item{Kind: "question", Held: true}, true},
		{Item{Kind: "question"}, false},
		{Item{Kind: "doctrine", Held: true}, false},
	} {
		if got := tc.i.PushEligible(now); got != tc.want {
			t.Fatalf("policy %+v = %t", tc.i, got)
		}
	}
	if (Item{Kind: "approval", Held: true, ExpiresAt: &now}).PushEligible(now) {
		t.Fatal("expired approval eligible")
	}
	f := setup(t)
	for _, path := range []string{"?limit=0", "?limit=101", "?cursor=invalid", "?cursor=" + fmt.Sprintf("%0600d", 1)} {
		r := httptest.NewRequest("GET", "/api/decision-desk/projection"+path, nil).WithContext(tenant.WithPrincipal(t.Context(), f.person))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != 400 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unbounded projection %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/decision-desk/projection", nil).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("agent received person projection")
	}
}
