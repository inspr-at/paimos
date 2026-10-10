// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestEvaluateRules(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		state, key, to, flag string
		days                 int
	}{{"new", "new", "", "triage_list", 7}, {"backlog", "backlog", "", "cancel_suggested", 90}, {"blocked", "blocked", "", "blocked_reminder", 14}, {"in_progress", "progress", "open", "", 3}, {"done", "done", "", "missed_release", 14}, {"delivered", "accept", "accepted", "", 30}} {
		t.Run(tc.key, func(t *testing.T) {
			s := Defaults()
			c := candidate{Node: node{State: tc.state}, Since: now.Add(-time.Duration(tc.days) * 24 * time.Hour), Activity: now.Add(-time.Duration(tc.days) * 24 * time.Hour), MergedAt: now.Add(-time.Duration(tc.days) * 24 * time.Hour), UsesReleases: true}
			if evaluate(c, s, now.Add(-time.Nanosecond)) != nil {
				t.Fatal("ran before threshold")
			}
			d := evaluate(c, s, now)
			if d == nil || d.Rule != tc.key || d.To != tc.to || d.Flag != tc.flag || d.Reason == "" {
				t.Fatalf("threshold decision: %+v", d)
			}
			r := s.Rules[tc.key]
			r.Enabled = false
			s.Rules[tc.key] = r
			if evaluate(c, s, now) != nil {
				t.Fatal("disabled rule ran")
			}
			s = Defaults()
			s.Enabled = false
			d = evaluate(c, s, now)
			if (tc.key == "new" || tc.key == "backlog") != (d != nil) {
				t.Fatal("Off must retain suggestions only")
			}
			human := "Touch ID"
			c.Node.HumanCheck = &human
			s = Defaults()
			d = evaluate(c, s, now)
			if tc.key == "accept" {
				if d == nil || !d.Skip || d.To != "" {
					t.Fatal("human check was accepted")
				}
			} else if d == nil || d.Skip {
				t.Fatal("human check suppressed a non-delivery rule")
			}
		})
	}
	for _, state := range []string{"open", "qa", "accepted", "cancelled", "archived", "custom", "queued"} {
		if evaluate(candidate{Node: node{State: state}, Since: now.Add(-400 * 24 * time.Hour)}, Defaults(), now) != nil {
			t.Fatalf("changed %s", state)
		}
	}
	c := candidate{Node: node{State: "in_progress", Fields: map[string]json.RawMessage{}}, Since: now.Add(-10 * 24 * time.Hour), Activity: now.Add(-10 * 24 * time.Hour), Work: true}
	if evaluate(c, Defaults(), now) != nil {
		t.Fatal("active session reopened")
	}
	c.Work = false
	c.WorkActivity = now.Add(-24 * time.Hour)
	if evaluate(c, Defaults(), now) != nil {
		t.Fatal("recent work reopened")
	}
	c.WorkActivity = now.Add(-10 * 24 * time.Hour)
	// Legacy fields are not authoritative evidence; neither they nor ticket
	// comments/title edits can keep an otherwise stalled work episode alive.
	c.Node.Fields["branch"] = json.RawMessage(`"work/ticket"`)
	c.Node.Fields["pr_url"] = json.RawMessage(`"https://example.test/pr/1"`)
	c.Activity = now
	if evaluate(c, Defaults(), now) == nil {
		t.Fatal("non-work activity suppressed reopening")
	}
	for _, state := range []string{"inprogress", " In -- Progress ", "IN-PROGRESS", "active", "progress"} {
		c.Node.State = state
		if evaluate(c, Defaults(), now) == nil {
			t.Fatalf("missed progress spelling %q", state)
		}
	}
	c.Node.State = "delivered"
	c.Objection = true
	if evaluate(c, Defaults(), now.Add(60*24*time.Hour)) != nil {
		t.Fatal("objection ignored")
	}
}
func TestSettingsValidation(t *testing.T) {
	s := Defaults()
	if s.Validate() != nil {
		t.Fatal("invalid defaults")
	}
	for _, key := range []string{"new", "backlog", "blocked", "progress", "done", "accept"} {
		for _, days := range []int{0, -1, 366} {
			s := Defaults()
			r := s.Rules[key]
			r.Days = days
			s.Rules[key] = r
			if s.Validate() == nil {
				t.Fatalf("accepted %s days=%d", key, days)
			}
		}
	}
	s = Defaults()
	delete(s.Rules, "new")
	if s.Validate() == nil {
		t.Fatal("missing rule")
	}
	s = Defaults()
	s.Rules["unknown"] = Rule{true, 10}
	if s.Validate() == nil {
		t.Fatal("unknown rule")
	}
	s = Defaults()
	s.Rules["publish"] = Rule{true, 1}
	if s.Validate() == nil {
		t.Fatal("publish days")
	}
}

type fixture struct {
	t       *testing.T
	d       *dbtest.DB
	p       tenant.Principal
	m       *Module
	project string
	now     time.Time
	h       http.Handler
}

func setup(t *testing.T) *fixture {
	f := &fixture{t: t, d: dbtest.Open(t), now: time.Now().UTC().Truncate(24 * time.Hour)}
	f.p = tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person, Name: "Owner", Roles: []string{"owner"}}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'autopilot','Autopilot')`, f.p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Owner',ARRAY['owner']) RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, f.p.TenantID, f.p.ID)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "owner")
	f.project = f.add("AUT-1", "project", "open", 1, nil)
	f.m = New(f.d.App)
	mux := http.NewServeMux()
	f.m.Mount(mux)
	events.New(f.d.App, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
	f.h = mux
	return f
}
func (f *fixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.p.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) add(key, kind, state string, days int, human *string) string {
	f.t.Helper()
	var id string
	var project any
	if kind != "project" {
		project = f.project
	}
	at := f.now.Add(-time.Duration(days) * 24 * time.Hour)
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,created_at,updated_at,human_check) SELECT $1,id,$2,'Ticket',$3,$4,$5,$5,$6 FROM node_kinds WHERE tenant_id=$1 AND slug=$7 RETURNING id::text`, f.p.TenantID, key, state, project, at, human, kind).Scan(&id)
	})
	return id
}
func (f *fixture) call(p tenant.Principal, method, path, body string, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != want {
		f.t.Fatalf("%s %s: %d %s; want %d", method, path, w.Code, w.Body, want)
	}
	return w
}
func (f *fixture) state(id string) node {
	var out node
	f.tx(func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(f.t.Context(), `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, id).Scan(&raw)
		if err == nil {
			err = json.Unmarshal(raw, &out)
		}
		return err
	})
	return out
}
func (f *fixture) changes(id string) []Change {
	var out []Change
	f.tx(func(tx pgx.Tx) error {
		var err error
		out, err = ChangesTx(f.t.Context(), tx, f.p, id, false, 0)
		return err
	})
	return out
}
func (f *fixture) run(day time.Time) {
	f.t.Helper()
	if err := f.m.RunTenant(f.t.Context(), f.p.TenantID, day); err != nil {
		f.t.Fatal(err)
	}
}
func TestDailyAuditUndoAndIdempotence(t *testing.T) {
	f := setup(t)
	ids := map[string]string{}
	for i, tc := range []struct {
		state string
		days  int
	}{{"new", 7}, {"backlog", 90}, {"blocked", 14}, {"in_progress", 3}, {"done", 14}, {"delivered", 30}, {"qa", 40}, {"accepted", 40}, {"cancelled", 40}, {"archived", 40}, {"open", 40}} {
		ids[tc.state] = f.add(fmt.Sprintf("AUT-%d", i+2), "work", tc.state, tc.days, nil)
	}
	f.merge(ids["done"], 14)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.m.RunTenant(context.Background(), f.p.TenantID, f.now) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for state, id := range ids {
		n := f.state(id)
		want := state
		if state == "in_progress" {
			want = "open"
		}
		if state == "delivered" {
			want = "accepted"
		}
		if n.State != want {
			t.Fatalf("%s became %s", state, n.State)
		}
	}
	for _, tc := range []struct{ state, flag string }{{"new", "triage_list"}, {"backlog", "cancel_suggested"}, {"blocked", "blocked_reminder"}, {"done", "missed_release"}} {
		if !f.state(ids[tc.state]).Marks[tc.flag] {
			t.Fatalf("missing %s", tc.flag)
		}
	}
	f.run(f.now)
	f.run(f.now.Add(24 * time.Hour))
	for _, state := range []string{"new", "backlog", "blocked", "done", "in_progress", "delivered"} {
		ch := f.changes(ids[state])
		if len(ch) != 1 || !ch[0].Undoable || ch[0].Reason == "" {
			t.Fatalf("%s changes %+v", state, ch)
		}
	}
	acceptedChange := f.changes(ids["delivered"])[0]
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", acceptedChange.EventID), "", 201)
	ch := f.changes(ids["in_progress"])[0]
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", ch.EventID), "", 201)
	if f.state(ch.NodeID).State != "in_progress" {
		t.Fatal("undo failed")
	}
	f.run(f.now.Add(2 * 24 * time.Hour))
	if f.state(ch.NodeID).State != "in_progress" {
		t.Fatal("job redid Undo")
	}
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", ch.EventID), "", 409)
	// Undo of flags also remains undone after a retry.
	ch = f.changes(ids["backlog"])[0]
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", ch.EventID), "", 201)
	f.run(f.now.Add(3 * 24 * time.Hour))
	if f.state(ch.NodeID).Marks["cancel_suggested"] {
		t.Fatal("suggestion redid Undo")
	}
	f.run(f.now.Add(40 * 24 * time.Hour))
	if f.state(ids["delivered"]).State != "delivered" {
		t.Fatal("acceptance Undo was not treated as an objection")
	}
}
func TestAutopilotHumanCheckObjectionAndStaleUndo(t *testing.T) {
	f := setup(t)
	human := "Touch ID"
	flagged := f.add("AUT-2", "work", "delivered", 35, &human)
	objection := f.add("AUT-3", "work", "delivered", 35, nil)
	stale := f.add("AUT-4", "work", "in_progress", 4, nil)
	f.tx(func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &objection, Type: "comment.created", After: map[string]string{"body_markdown": "This still fails."}})
		return err
	})
	f.run(f.now)
	f.run(f.now.Add(24 * time.Hour))
	if f.state(flagged).State != "delivered" || f.state(objection).State != "delivered" {
		t.Fatal("human check or objection ignored")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='status_autopilot.skipped'`, flagged).Scan(&count)
		if count != 1 {
			return fmt.Errorf("skip repeated %d times", count)
		}
		return err
	})
	ch := f.changes(stale)[0]
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Later edit',updated_at=clock_timestamp() WHERE id=$1`, stale)
		return err
	})
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", ch.EventID), "", 409)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET human_check=NULL WHERE id=$1`, flagged)
		return err
	})
	f.run(f.now.Add(2 * 24 * time.Hour))
	if f.state(flagged).State != "accepted" {
		t.Fatal("checked flag never resumed")
	}
}
func TestSettingsAndOverrides(t *testing.T) {
	f := setup(t)
	var s Settings
	_ = json.Unmarshal(f.call(f.p, "GET", "/api/settings/status-autopilot", "", 200).Body.Bytes(), &s)
	if s.Revision != 0 || !s.Enabled || s.Rules["accept"].Days != 30 {
		t.Fatalf("defaults %+v", s)
	}
	write := func(enabled bool, rev int64) string {
		b, _ := json.Marshal(map[string]any{"enabled": enabled, "rules": s.Rules, "expected_revision": rev})
		return string(b)
	}
	f.call(f.p, "PUT", "/api/settings/status-autopilot", write(false, 0), 200)
	f.call(f.p, "PUT", "/api/settings/status-autopilot", write(true, 0), 409)
	f.call(f.p, "PUT", "/api/settings/status-autopilot", `{"enabled":true}`, 400)
	f.call(f.p, "PUT", "/api/settings/status-autopilot", `{"enabled":true,"rules":{},"expected_revision":1}`, 400)
	agent := f.p
	agent.Kind = tenant.Agent
	f.call(agent, "PUT", "/api/settings/status-autopilot", write(true, 1), 403)
	member := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person, Name: "Member", Roles: []string{"member"}}
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Member',ARRAY['member']) RETURNING id::text`, member.TenantID).Scan(&member.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, member.TenantID, member.ID)
	})
	f.call(member, "GET", "/api/settings/status-autopilot", "", 200)
	f.call(member, "PUT", "/api/settings/status-autopilot", write(true, 1), 403)
	path := "/api/projects/" + f.project + "/status-autopilot"
	f.call(f.p, "PUT", path, `{"mode":"on","expected_revision":0}`, 200)
	f.call(f.p, "PUT", path, `{"mode":"off","expected_revision":0}`, 409)
	f.call(member, "PUT", path, `{"mode":"off","expected_revision":1}`, 403)
	var o Override
	_ = json.Unmarshal(f.call(f.p, "GET", path, "", 200).Body.Bytes(), &o)
	if !o.Effective || o.Mode != "on" {
		t.Fatalf("override %+v", o)
	}
	id := f.add("AUT-2", "work", "in_progress", 4, nil)
	f.run(f.now)
	if f.state(id).State != "open" {
		t.Fatal("project On did not override workspace Off")
	}
	f.call(f.p, "PUT", path, `{"mode":"off","expected_revision":1}`, 200)
	id = f.add("AUT-3", "work", "in_progress", 4, nil)
	f.run(f.now.Add(24 * time.Hour))
	if f.state(id).State != "in_progress" {
		t.Fatal("project Off ignored")
	}
	f.call(f.p, "PUT", path, `{"mode":"inherit","expected_revision":2}`, 200)
	_ = json.Unmarshal(f.call(f.p, "GET", path, "", 200).Body.Bytes(), &o)
	if o.Effective {
		t.Fatal("inherit ignored workspace Off")
	}
}
func (f *fixture) release(ids []string) string {
	var release string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, f.p.TenantID, f.project); err != nil {
			return err
		}
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id) SELECT $1,id,'REL-1','Azimuth','done',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='release' RETURNING id::text`, f.p.TenantID, f.project).Scan(&release); err != nil {
			return err
		}
		if _, err := tx.Exec(f.t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at,version_scheme,version) VALUES($1,$2,$3,1,'released',clock_timestamp(),'inspr-calendar-v2','260930120000.0.0')`, f.p.TenantID, release, f.project); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(f.t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.p.TenantID, id, f.project, release); err != nil {
				return err
			}
		}
		return nil
	})
	return release
}
func TestReleasePublishHumanCheckReplayUndo(t *testing.T) {
	f := setup(t)
	human := "Touch ID"
	done := f.add("AUT-2", "work", "done", 15, nil)
	flagged := f.add("AUT-3", "work", "done", 15, &human)
	cancelled := f.add("AUT-4", "work", "cancelled", 15, nil)
	accepted := f.add("AUT-5", "work", "accepted", 15, nil)
	unshipped := map[string]string{}
	for i, state := range []string{"new", "backlog", "open", "blocked", "in_progress", "qa", "archived"} {
		unshipped[state] = f.add(fmt.Sprintf("AUT-%d", i+6), "work", state, 15, nil)
	}
	content := f.add("AUT-1091", "work", "done", 15, nil)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"no_release_needed":true}'::jsonb WHERE id=$1`, content)
		return err
	})
	ids := []string{done, flagged, cancelled, accepted, content}
	for _, id := range unshipped {
		ids = append(ids, id)
	}
	release := f.release(ids)
	publish := func() { f.tx(func(tx pgx.Tx) error { return PublishTx(t.Context(), tx, f.p.TenantID, release) }) }
	publish()
	publish()
	if f.state(done).State != "delivered" || f.state(flagged).State != "done" || f.state(cancelled).State != "cancelled" || f.state(accepted).State != "accepted" {
		t.Fatal("publish status/flag failure")
	}
	if n := f.state(content); n.State != "done" || len(f.changes(content)) != 0 {
		t.Fatalf("content sent through release publication: %+v", n)
	}
	for state, id := range unshipped {
		if f.state(id).State != state {
			t.Fatalf("release shipped unfinished %s", state)
		}
	}
	ch := f.changes(done)
	if len(ch) != 1 || !strings.Contains(ch[0].Reason, "Azimuth") || !strings.Contains(ch[0].Reason, "260930120000.0.0") {
		t.Fatalf("missing release evidence %+v", ch)
	}
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", ch[0].EventID), "", 201)
	publish()
	if f.state(done).State != "done" {
		t.Fatal("publish replay redid Undo")
	}
	// A fresh Done episode after publication is work for a later release.
	later := f.add("AUT-20", "work", "done", 15, nil)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.p.TenantID, later, f.project, release); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &later, Type: "node.updated", Before: map[string]string{"state": "open"}, After: map[string]string{"state": "done"}})
		return err
	})
	publish()
	if f.state(later).State != "done" {
		t.Fatal("historical publish shipped a later Done episode")
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET human_check=NULL WHERE id=$1`, flagged)
		return err
	})
	f.run(f.now)
	if f.state(flagged).State != "delivered" {
		t.Fatal("human-checked delivery did not resume")
	}
}
func TestAutopilotTenantIsolation(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "work", "in_progress", 4, nil)
	f.run(f.now)
	foreign := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000002", Kind: tenant.Person, Name: "Other", Roles: []string{"owner"}}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'other','Other')`, foreign.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other',ARRAY['owner']) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, foreign.TenantID, foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, foreign.TenantID, foreign.ID, "owner")
	f.call(foreign, "GET", "/api/projects/"+f.project+"/status-autopilot", "", 404)
	w := f.call(foreign, "GET", "/api/status-autopilot/changes", "", 200)
	if strings.Contains(w.Body.String(), id) {
		t.Fatal("foreign event leak")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_receipts`).Scan(&count)
		if count != 0 {
			return fmt.Errorf("foreign receipts visible")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: content work reports false completion, bypasses a human hold, or waits
// forever for a release. Injected time proves the acceptance boundary.
func TestNoReleaseNeededAcceptancePolicy(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	settings := Defaults()
	settings.Rules["accept"] = Rule{Enabled: true, Days: 1}
	c := candidate{Node: node{State: "done", Fields: map[string]json.RawMessage{"no_release_needed": json.RawMessage(`true`), "merged_at": json.RawMessage(`"2026-10-10T00:00:00Z"`)}, Marks: map[string]bool{"missed_release": true}}, Since: now.Add(-24 * time.Hour)}
	if d := evaluate(c, settings, now.Add(-time.Nanosecond)); d != nil {
		t.Fatalf("accepted before one day: %+v", d)
	}
	d := evaluate(c, settings, now)
	if d == nil || d.Rule != "accept" || d.To != "accepted" || d.Flag != "" || d.Skip || !strings.Contains(d.Reason, "no release needed") {
		t.Fatalf("content decision: %+v", d)
	}
	c.Objection = true
	if d := evaluate(c, settings, now.Add(60*24*time.Hour)); d != nil {
		t.Fatalf("objection ignored or marked as missed release: %+v", d)
	}
	c.Objection = false
	c.Node.Fields["delivery_objection"] = json.RawMessage(`"Scene needs another edit"`)
	if d := evaluate(c, settings, now); d != nil {
		t.Fatalf("stored objection ignored: %+v", d)
	}
	delete(c.Node.Fields, "delivery_objection")
	check := "Inspect the scene"
	c.Node.HumanCheck = &check
	if d := evaluate(c, settings, now.Add(60*24*time.Hour)); d == nil || !d.Skip || d.To != "" || d.Flag != "" {
		t.Fatalf("human check bypassed: %+v", d)
	}
	c.Node.HumanCheck = nil
	for _, rule := range []string{"accept", "workspace"} {
		off := Defaults()
		off.Rules["accept"] = Rule{Enabled: rule != "accept", Days: 1}
		off.Enabled = rule != "workspace"
		if d := evaluate(c, off, now); d != nil {
			t.Fatalf("disabled %s bypassed: %+v", rule, d)
		}
	}
	settings.Rules["accept"] = Rule{Enabled: true, Days: 2}
	if d := evaluate(c, settings, now); d != nil {
		t.Fatalf("ignored configured two-day policy: %+v", d)
	}
	c.Node.Marks = nil
	c.MergedAt, c.UsesReleases = c.Since, true
	delete(c.Node.Fields, "merged_at")
	for _, mark := range []string{`false`, `"true"`, `null`} {
		c.Node.Fields["no_release_needed"] = json.RawMessage(mark)
		if d := evaluate(c, settings, now); d != nil {
			t.Fatalf("unmarked work accepted: %+v", d)
		}
		if d := evaluate(c, settings, c.Since.Add(14*24*time.Hour)); d == nil || d.Rule != "done" || d.Flag != "missed_release" || d.To != "" {
			t.Fatalf("software release path changed: %+v", d)
		}
	}
}

func TestNoReleaseNeededDailyAcceptanceAndObjections(t *testing.T) {
	f := setup(t)
	settings := Defaults()
	settings.Rules["accept"] = Rule{Enabled: true, Days: 1}
	body, err := json.Marshal(map[string]any{"enabled": true, "rules": settings.Rules, "expected_revision": 0})
	if err != nil {
		t.Fatal(err)
	}
	f.call(f.p, "PUT", "/api/settings/status-autopilot", string(body), 200)
	check := "Inspect the content"
	accepted := f.add("AUT-1091", "work", "done", 1, nil)
	human := f.add("AUT-1092", "work", "done", 40, &check)
	objection := f.add("AUT-1093", "work", "done", 40, nil)
	software := f.add("AUT-1094", "work", "done", 1, nil)
	f.merge(software, 1)
	for _, id := range []string{accepted, human, objection} {
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"no_release_needed":true}'::jsonb WHERE id=$1`, id)
			return err
		})
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &objection, Type: "comment.created", After: map[string]string{"body_markdown": "The scene still needs changes."}})
		return err
	})
	f.run(f.now)
	if n := f.state(accepted); n.State != "accepted" || n.Marks["missed_release"] {
		t.Fatalf("marked done did not accept: %+v", n)
	}
	changes := f.changes(accepted)
	if len(changes) != 1 || changes[0].Rule != "accept" || !strings.Contains(changes[0].Reason, "no release needed") {
		t.Fatalf("missing acceptance audit: %+v", changes)
	}
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", changes[0].EventID), "", 201)
	f.run(f.now.Add(24 * time.Hour))
	for _, id := range []string{accepted, human, objection, software} {
		if n := f.state(id); n.State != "done" || n.Marks["missed_release"] {
			t.Fatalf("hold, objection or software path bypassed: %+v", n)
		}
	}
	f.tx(func(tx pgx.Tx) error {
		var skips int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='status_autopilot.skipped' AND metadata->>'rule'='accept'`, human).Scan(&skips); err != nil {
			return err
		}
		if skips != 1 {
			return fmt.Errorf("human-check skip audit: %d", skips)
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET human_check=NULL WHERE id=$1`, human)
		return err
	})
	f.run(f.now.Add(2 * 24 * time.Hour))
	if n := f.state(human); n.State != "accepted" {
		t.Fatalf("completed check never resumed: %+v", n)
	}
	f.run(f.now.Add(60 * 24 * time.Hour))
	for _, id := range []string{accepted, objection} {
		if n := f.state(id); n.State != "done" || n.Marks["missed_release"] {
			t.Fatalf("content falsely flagged for release: %+v", n)
		}
	}
	if n := f.state(software); n.State != "done" || !n.Marks["missed_release"] {
		t.Fatalf("software release flag changed: %+v", n)
	}
}

func (f *fixture) merge(id string, days int) {
	f.t.Helper()
	var usesReleases bool
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT EXISTS(SELECT 1 FROM journey_releases WHERE project_node_id=$1)`, f.project).Scan(&usesReleases)
	})
	if !usesReleases {
		f.release(nil)
	}
	f.tx(func(tx pgx.Tx) error {
		var snapshot json.RawMessage
		if err := tx.QueryRow(f.t.Context(), `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, id).Scan(&snapshot); err != nil {
			return err
		}
		meta, err := json.Marshal(map[string]any{"merged_at": f.now.Add(-time.Duration(days) * 24 * time.Hour), "merge_sha": strings.Repeat("a", 40), "pull_request": 1})
		if err != nil {
			return err
		}
		_, err = events.Append(f.t.Context(), tx, f.p, events.Change{NodeID: &id, Type: "delivery.merge_done", Before: snapshot, After: snapshot, Metadata: meta})
		return err
	})
}

// Risk: import timestamps, editable fields and time in Done create false release
// reminders. Recorded delivery evidence alone supplies the merge clock.
func TestMissedReleaseUsesRecordedMergeEvidence(t *testing.T) {
	f := setup(t)
	for i, tc := range []struct {
		name      string
		mergeDays int
		fields    string
		want      bool
	}{
		{"classic with merge", 20, `{"classic":{}}`, false},
		{"import without merge", 0, `{"classic":{}}`, false},
		{"done without merge", 0, `{}`, false},
		{"forged field without merge", 0, `{"merged_at":"2020-01-01T00:00:00Z"}`, false},
		{"recent merge and old Done", 13, `{}`, false},
		{"merged and unreleased", 14, `{}`, true},
	} {
		id := f.add(fmt.Sprintf("AUT-%d", i+2000), "work", "done", 100, nil)
		f.tx(func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2 WHERE id=$1`, id, tc.fields); err != nil {
				return err
			}
			_, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &id, Type: "import.node_created", After: map[string]string{"state": "done"}})
			return err
		})
		if tc.mergeDays > 0 {
			f.merge(id, tc.mergeDays)
		}
		f.tx(func(tx pgx.Tx) error {
			c, _, err := loadCandidate(t.Context(), tx, id, f.now)
			if err != nil {
				return err
			}
			if !c.Since.Equal(f.now.Add(-100 * 24 * time.Hour)) {
				t.Fatalf("%s: import became Done episode: %s", tc.name, c.Since)
			}
			d := evaluate(c, Defaults(), f.now)
			if (d != nil) != tc.want {
				t.Fatalf("%s: decision %+v", tc.name, d)
			}
			if tc.want {
				if d.Anchor != f.now.Add(-14*24*time.Hour).Format(time.RFC3339Nano) {
					t.Fatalf("wrong merge anchor: %+v", d)
				}
				c.UsesReleases = false
				if evaluate(c, Defaults(), f.now) != nil {
					t.Fatal("project without releases flagged")
				}
				c.UsesReleases, c.Released = true, true
				if evaluate(c, Defaults(), f.now) != nil {
					t.Fatal("published ticket flagged")
				}
			}
			return nil
		})
	}
	f.run(f.now)
	var missed int
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE status_autopilot->'missed_release'='true'::jsonb`).Scan(&missed)
	})
	if missed != 1 {
		t.Fatalf("daily missed count: %d", missed)
	}
}
