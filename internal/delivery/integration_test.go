// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fakeGitHub struct {
	pulls map[int64]Pull
	err   error
}

func (g *fakeGitHub) Pull(_ context.Context, n int64) (Pull, error) {
	if g.err != nil {
		return Pull{}, g.err
	}
	p, ok := g.pulls[n]
	if !ok {
		return Pull{}, errRead
	}
	return p, nil
}
func (g *fakeGitHub) OpenPulls(context.Context) ([]Pull, error) {
	if g.err != nil {
		return nil, g.err
	}
	out := []Pull{}
	for _, p := range g.pulls {
		if p.Open {
			out = append(out, p)
		}
	}
	return out, nil
}
func (g *fakeGitHub) Group(context.Context, string, string, string) ([]Pull, error) {
	return g.OpenPulls(context.Background())
}

type fixture struct {
	d                                      *dbtest.DB
	m                                      *Module
	gh                                     *fakeGitHub
	mux                                    *http.ServeMux
	person, agent, foreign                 tenant.Principal
	ticket, project, buildOrder, authorRun string
	at                                     time.Time
	fanouts                                int
	fanoutErr                              error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	f := &fixture{d: d, gh: &fakeGitHub{pulls: map[int64]Pull{}}, mux: http.NewServeMux(), at: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)}
	for i, p := range []*tenant.Principal{&f.person, &f.foreign} {
		p.Kind = tenant.Person
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Delivery fixture') RETURNING id::text`, fmt.Sprintf("delivery-%d", i)).Scan(&p.TenantID); err != nil {
			t.Fatal(err)
		}
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Delivery person') RETURNING id::text`, p.TenantID).Scan(&p.ID)
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, p.TenantID, p.ID, "admin")
	}
	f.agent = tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: []string{"delivery.read", "delivery.manage"}}
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Delivery agent') RETURNING id::text`, f.person.TenantID).Scan(&f.agent.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'AEON-1',id,'Delivery project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&f.project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title) SELECT $1,$2,'AEON-848',id,'Delivery ticket' FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, f.project).Scan(&f.ticket); err != nil {
			return err
		}
		return nil
	})
	dbtest.BindRole(t, d, f.agent.TenantID, f.agent.ID, "admin")
	config := crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: f.person.TenantID, Repository: "example/delivery", KeyFile: "/unused/test-fixture.pem"}
	f.m = New(d.App, config, []byte(strings.Repeat("s", 32)), f.gh, func(context.Context, []byte) error { f.fanouts++; return f.fanoutErr })
	f.m.now = func() time.Time { return f.at }
	f.m.Mount(f.mux)
	return f
}
func (f *fixture) tx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, body any, want int, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d != %d: %s", method, path, w.Code, want, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *fixture) item(t *testing.T, n int64) Item {
	t.Helper()
	var out *Item
	f.tx(t, func(tx pgx.Tx) error {
		var err error
		pr := n
		out, err = load(t.Context(), tx, stableID(f.person.TenantID, f.m.config.Repository, subject(&pr, nil)))
		return err
	})
	if out == nil {
		t.Fatal("delivery row missing")
	}
	return *out
}
func (f *fixture) pull() Pull {
	return Pull{Number: 7, Title: "AEON-848: delivery", Branch: "work/aeon-848-delivery", Head: strings.Repeat("b", 40), Base: strings.Repeat("a", 40), Open: true}
}
func (f *fixture) webhook(t *testing.T, event, action, id string, want int) {
	t.Helper()
	p := f.gh.pulls[7]
	body := map[string]any{"action": action, "installation": map[string]any{"id": 456}, "repository": map[string]any{"full_name": f.m.config.Repository, "default_branch": "main"}, "pull_request": map[string]any{"number": 7, "head": map[string]any{"sha": p.Head}, "base": map[string]any{"sha": p.Base, "repo": map[string]any{"full_name": f.m.config.Repository}}}, "merge_group": map[string]any{"head_sha": strings.Repeat("c", 40), "base_sha": p.Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-group"}, "check_run": map[string]any{"head_sha": p.Head, "pull_requests": []any{map[string]int{"number": 7}}}, "sha": p.Head}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-GitHub-Event", event)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("webhook %s/%s: %d != %d", event, action, w.Code, want)
	}
}
func (f *fixture) build(t *testing.T) {
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title) SELECT $1,$2,'AEON-849',id,'Build order' FROM node_kinds WHERE slug='work_order' RETURNING id::text`, f.person.TenantID, f.ticket).Scan(&f.buildOrder); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,kind,status) VALUES($1,$2,$3,'build','done')`, f.person.TenantID, f.buildOrder, f.person.ID); err != nil {
			return err
		}
		var profile string
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'delivery-author','test','codex','openai','gpt-6.1','high','frontier') RETURNING id::text`, f.person.TenantID).Scan(&profile); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status) VALUES($1,$2,$3,$4,'completed') RETURNING id::text`, f.person.TenantID, f.buildOrder, f.agent.ID, profile).Scan(&f.authorRun)
	})
}
func (f *fixture) review(t *testing.T) {
	f.tx(t, func(tx pgx.Tx) error {
		var order, run, profile, evidence string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title) SELECT $1,$2,'AEON-850',id,'Review order' FROM node_kinds WHERE slug='work_order' RETURNING id::text`, f.person.TenantID, f.ticket).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,kind,status) VALUES($1,$2,$3,'review','done')`, f.person.TenantID, order, f.person.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'delivery-reviewer','test','claude','anthropic','claude-opus-4-6','xhigh','frontier') RETURNING id::text`, f.person.TenantID).Scan(&profile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,effective_model,model_evidence) VALUES($1,$2,$3,$4,'completed','claude-opus-4-6','vendor_reported') RETURNING id::text`, f.person.TenantID, order, f.agent.ID, profile).Scan(&run); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,run_id,submitted_by_principal_id,kind,reference) VALUES($1,$2,$3,$4,'text','VERDICT: ok') RETURNING id::text`, f.person.TenantID, order, run, f.person.ID).Scan(&evidence); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,ticket_snapshot,repository,base_sha,head_sha,author_run_id,author_family,reviewer_profile_id,reviewer_family,run_id,evidence_id,result) VALUES($1,$2,$3,gen_random_uuid(),'{}','delivery fixture',$4,$5,$6,$7,'openai',$8,'anthropic',$9,$10,'{"verdict":"ok","findings":[],"reason":""}')`, f.person.TenantID, order, f.ticket, f.m.config.Repository, strings.Repeat("a", 40), strings.Repeat("b", 40), f.authorRun, profile, run, evidence)
		return err
	})
}
func TestDeliveryWebhookSequenceRebuildIsolationAndHolds(t *testing.T) {
	f := newFixture(t)
	f.build(t)
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	var page Page
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/delivery", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].State != Built || page.Items[0].PR != nil {
		t.Fatal("build run not projected")
	}
	f.review(t)
	f.at = f.at.Add(time.Minute)
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/delivery", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].State != Reviewed {
		t.Fatalf("review did not project exact verified gate: %+v", page.Items)
	}
	f.gh.pulls[7] = f.pull()
	f.at = f.at.Add(time.Minute)
	f.webhook(t, "pull_request", "opened", "open-1", 204)
	i := f.item(t, 7)
	if i.State != Pushed || i.Owner != "ci" || i.LinkSource == nil || *i.LinkSource != "title_key" {
		t.Fatalf("wrong pushed link: %+v", i)
	}
	f.webhook(t, "pull_request", "opened", "open-1", 204)
	if f.fanouts != 1 {
		t.Fatal("replay repeated completed fan-out")
	}
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/delivery", nil, 200, &page)
	if len(page.Items) != 1 {
		t.Fatal("pre-PR placeholder remained")
	}
	p := f.gh.pulls[7]
	for _, n := range *defaults().RequiredChecks {
		p.Checks = append(p.Checks, Check{Name: n, Status: "completed", Conclusion: "success"})
	}
	f.gh.pulls[7] = p
	f.at = f.at.Add(time.Minute)
	f.webhook(t, "check_run", "completed", "checks-1", 204)
	if i = f.item(t, 7); i.State != CIGreen || i.Owner != "coordinator" {
		t.Fatal("checks did not grant observed green")
	}
	f.at = f.at.Add(time.Minute)
	f.webhook(t, "merge_group", "checks_requested", "queue-1", 204)
	if i = f.item(t, 7); i.State != InQueue || i.Owner != "queue" {
		t.Fatal("queue group missing")
	}
	f.at = f.at.Add(time.Minute)
	f.webhook(t, "merge_group", "destroyed", "destroy-1", 204)
	if f.item(t, 7).State != CIGreen {
		t.Fatal("destroyed queue remained queued")
	}
	f.tx(t, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		return f.m.QueueFailedTx(t.Context(), tx, f.person.TenantID, i.ID, f.at)
	})
	if i = f.item(t, 7); i.State != QueueFailed || i.Owner != "builder" {
		t.Fatal("failure hook missing")
	}
	f.call(t, f.person, "POST", "/api/delivery/"+i.ID+"/hold", map[string]string{"reason": "release freeze"}, 200, &i)
	if i.State != Held || i.Owner != "person" || i.Deadline != nil {
		t.Fatal("hold failed")
	}
	f.call(t, f.person, "DELETE", "/api/delivery/"+i.ID+"/hold", nil, 200, &i)
	if i.State != QueueFailed {
		t.Fatal("release did not restore current facts")
	}
	f.call(t, f.agent, "POST", "/api/delivery/"+i.ID+"/hold", map[string]string{"reason": "agent hold"}, 403, nil)
	f.call(t, f.foreign, "GET", "/api/delivery", nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("other tenant saw delivery")
	}
	if err := db.InTenant(db.AllProjects(t.Context(), "delivery RLS test"), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_items WHERE tenant_id=$1`, f.person.TenantID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("tenant RLS leaked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.Merged = true
	p.Open = false
	f.gh.pulls[7] = p
	f.at = f.at.Add(time.Minute)
	f.webhook(t, "pull_request", "closed", "merge-1", 204)
	before := f.item(t, 7)
	if before.State != Merged || before.Deadline != nil {
		t.Fatal("merged not terminal")
	}
	f.call(t, f.person, "POST", "/api/delivery/"+before.ID+"/hold", map[string]string{"reason": "too late"}, 409, nil)
	var eventCount int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.state_changed'`).Scan(&eventCount)
	})
	if err := f.m.Rebuild(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	after := f.item(t, 7)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("replay mismatch\nbefore %+v\nafter %+v", before, after)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.state_changed'`).Scan(&count); err != nil {
			return err
		}
		if count != eventCount {
			t.Fatal("rebuild duplicated audit")
		}
		return nil
	})
}
func TestDeliveryReconciliationHealsMissedWebhooksAndFanoutRetry(t *testing.T) {
	f := newFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	f.fanoutErr = errors.New("simulated legacy outage")
	f.webhook(t, "pull_request", "opened", "retry-1", 502)
	if f.fanouts != 1 {
		t.Fatal("fan-out not attempted")
	}
	f.fanoutErr = nil
	f.webhook(t, "pull_request", "opened", "retry-1", 204)
	if f.fanouts != 2 {
		t.Fatal("deduplication lost failed fan-out")
	}
	p.Head = strings.Repeat("d", 40)
	for _, n := range *defaults().RequiredChecks {
		p.Checks = append(p.Checks, Check{Name: n, Status: "completed", Conclusion: "success"})
	}
	f.gh.pulls[7] = p
	f.at = f.at.Add(time.Minute)
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	i := f.item(t, 7)
	if i.Head != p.Head || i.State != CIGreen {
		t.Fatal("missed head/check webhook not healed")
	}
	since := i.Since
	f.at = f.at.Add(time.Minute)
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if !f.item(t, 7).Since.Equal(since) {
		t.Fatal("poll extended deadline")
	}
	p.Queued = true
	p.QueueHead = strings.Repeat("e", 40)
	f.gh.pulls[7] = p
	f.at = f.at.Add(time.Minute)
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if f.item(t, 7).State != InQueue {
		t.Fatal("missed enqueue webhook not healed")
	}
	p.Queued = false
	p.QueueHead = ""
	f.gh.pulls[7] = p
	f.at = f.at.Add(time.Minute)
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if f.item(t, 7).State != CIGreen {
		t.Fatal("missed dequeue webhook not healed")
	}
	f.gh.err = errRead
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); !errors.Is(err, errRead) {
		t.Fatal("partial GitHub read reported success")
	}
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		return authz.RequireTx(t.Context(), tx, f.agent, "delivery.manage", authz.Scope{})
	}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("built-in admin agent acquired delivery.manage", err)
	}
}
func TestDeliverySettingsAndIngressBinding(t *testing.T) {
	f := newFixture(t)
	var s Settings
	f.call(t, f.person, "PUT", "/api/settings/delivery", map[string]any{"required_checks": []string{"go"}, "deadlines": map[string]int{"pushed": 2}}, 200, &s)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{"deadlines": map[string]int{"reviewed": 3}}, 200, &s)
	if len(*s.RequiredChecks) != 1 || s.Deadlines[Pushed] != 2 || s.Deadlines[Reviewed] != 3 {
		t.Fatal("settings inheritance failed")
	}
	f.call(t, f.person, "POST", "/api/delivery/not-a-uuid/hold", map[string]string{"reason": ""}, 400, nil)
	for _, sig := range []string{"", "sha256=" + strings.Repeat("0", 64)} {
		r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(`{}`))
		r.Header.Set("X-Hub-Signature-256", sig)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("signature refused with wrong status")
		}
	}
	raw := []byte(`{"action":"opened","installation":{"id":999},"repository":{"full_name":"other/repo"}}`)
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	r.Header.Set("X-GitHub-Delivery", "wrong-installation")
	r.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("unknown installation not rejected")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_github_events`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("unknown installation stored")
		}
		return nil
	})
}
