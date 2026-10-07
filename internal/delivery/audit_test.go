// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

type auditFake struct {
	*fakeGitHub
	commits    []string
	facts      map[string]*MergeFact
	checks     map[string][]Check
	checksErr  error
	checkReads int
}

func (g *auditFake) AuditCommits(context.Context) ([]string, error) { return g.commits, g.err }
func (g *auditFake) AuditCommit(_ context.Context, sha string) (*MergeFact, error) {
	return g.facts[sha], g.err
}
func (g *auditFake) AuditChecks(_ context.Context, sha string) ([]Check, error) {
	g.checkReads++
	return g.checks[sha], g.checksErr
}
func auditGreen() []Check {
	out := []Check{}
	for _, name := range *defaults().RequiredChecks {
		out = append(out, Check{Name: name, Status: "completed", Conclusion: "success"})
	}
	return out
}
func auditSend(t *testing.T, f *fixture, name, id string, body map[string]any, want int) {
	t.Helper()
	body["installation"] = map[string]any{"id": 456}
	body["repository"] = map[string]any{"full_name": f.m.config.Repository, "default_branch": "main"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-GitHub-Event", name)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("audit webhook %s: %d != %d", id, w.Code, want)
	}
}
func auditPRBody(f *fixture, fact MergeFact) map[string]any {
	p := f.pull()
	return map[string]any{"action": "closed", "pull_request": map[string]any{"number": *fact.PR, "title": fact.Title, "merged": true, "merge_commit_sha": fact.SHA, "merged_at": fact.At, "merged_by": map[string]string{"login": fact.MergedBy}, "head": map[string]string{"sha": fact.Head, "ref": fact.Branch}, "base": map[string]any{"sha": p.Base, "ref": "main", "repo": map[string]string{"full_name": f.m.config.Repository}}}}
}
func auditRead(t *testing.T, f *fixture, sha string) MergeAudit {
	t.Helper()
	var a MergeAudit
	f.tx(t, func(tx pgx.Tx) error {
		var err error
		a, err = scanAudit(tx.QueryRow(t.Context(), `SELECT `+auditColumns+` FROM delivery_merge_audit WHERE merge_sha=$1`, sha))
		return err
	})
	return a
}
func auditCounts(t *testing.T, f *fixture) (rows, events, alerts int) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_merge_audit),(SELECT count(*) FROM events WHERE type='delivery.bypass_merge'),(SELECT count(*) FROM inbox_messages WHERE sender_label='Delivery')`).Scan(&rows, &events, &alerts)
	})
	return
}

func TestMergeAuditWebhookReconciliationAndIsolation(t *testing.T) {
	// Risks: false review/check/queue provenance, duplicate bypass alerts,
	// missed merges, and tenant/project leaks at the new audit boundary.
	f := newFixture(t)
	f.build(t)
	f.review(t)
	g := &auditFake{fakeGitHub: f.gh, facts: map[string]*MergeFact{}, checks: map[string][]Check{}}
	f.m.github = g
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state) VALUES($1,$2,$3,'paused')`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	p := f.pull()
	p.Open = false
	p.Merged = true
	f.gh.pulls[7] = p
	pr := int64(7)
	queue := MergeFact{SHA: strings.Repeat("c", 40), Head: p.Head, Title: p.Title, Branch: p.Branch, MergedBy: "github-merge-queue[bot]", PR: &pr, At: f.at}
	g.checks[queue.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "audit-queue", auditPRBody(f, queue), 204)
	a := auditRead(t, f, queue.SHA)
	if len(a.Flags) != 0 || !a.ViaQueue || !a.ChecksPassed || !a.ReviewOK || a.Ticket == nil || *a.Ticket != f.ticket {
		t.Fatalf("queue provenance failed: %+v", a)
	}
	if _, events, alerts := auditCounts(t, f); events != 0 || alerts != 0 {
		t.Fatal("clean merge produced bypass effects")
	}

	admin := queue
	admin.SHA = strings.Repeat("d", 40)
	admin.MergedBy = "admin"
	admin.At = f.at.Add(time.Minute)
	g.checks[admin.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "audit-admin", auditPRBody(f, admin), 204)
	a = auditRead(t, f, admin.SHA)
	if !reflect.DeepEqual(a.Flags, []string{"no_queue"}) || a.AlertedAt == nil {
		t.Fatalf("admin bypass not notified: %+v", a)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var node, recipient, actor, key string
		if err := tx.QueryRow(t.Context(), `SELECT node_id::text,actor_principal_id::text FROM events WHERE type='delivery.bypass_merge'`).Scan(&node, &actor); err != nil {
			return err
		}
		if node != f.ticket {
			t.Fatal("bypass event not linked to ticket")
		}
		if err := tx.QueryRow(t.Context(), `SELECT recipient_principal_id::text,sender_principal_id::text,idempotency_key FROM inbox_messages WHERE sender_label='Delivery'`).Scan(&recipient, &node, &key); err != nil {
			return err
		}
		if recipient != f.person.ID || node != actor || key != digest([]byte(f.person.TenantID+"|"+f.m.config.Repository+"|"+admin.SHA)) {
			t.Fatal("wrong lead/System actor/idempotency identity")
		}
		return nil
	})
	auditSend(t, f, "pull_request", "audit-admin", auditPRBody(f, admin), 204)
	auditSend(t, f, "pull_request", "audit-admin-redelivery", auditPRBody(f, admin), 204)
	if rows, events, alerts := auditCounts(t, f); rows != 2 || events != 1 || alerts != 1 {
		t.Fatalf("replay duplicated effects: %d %d %d", rows, events, alerts)
	}

	stale := queue
	stale.SHA = strings.Repeat("e", 40)
	stale.Head = strings.Repeat("a", 40)
	g.checks[stale.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "audit-stale-head", auditPRBody(f, stale), 204)
	if a = auditRead(t, f, stale.SHA); !reflect.DeepEqual(a.Flags, []string{"no_review"}) {
		t.Fatalf("wrong head satisfied review: %+v", a)
	}

	direct := MergeFact{SHA: strings.Repeat("f", 40), Head: strings.Repeat("f", 40), MergedBy: "admin", At: f.at}
	g.facts[direct.SHA] = &direct
	auditSend(t, f, "push", "audit-direct", map[string]any{"ref": "refs/heads/main", "after": direct.SHA}, 204)
	if a = auditRead(t, f, direct.SHA); !slices.Contains(a.Flags, "direct_push") || !slices.Contains(a.Flags, "checks_missing") || a.PR != nil || a.AlertedAt != nil {
		t.Fatalf("direct push hidden or guessed a lead: %+v", a)
	}
	// A push of an already audited PR merge must not become a direct push.
	g.facts[queue.SHA] = &queue
	auditSend(t, f, "push", "audit-queue-push", map[string]any{"ref": "refs/heads/main", "after": queue.SHA}, 204)

	missed := admin
	missed.SHA = strings.Repeat("1", 40)
	g.commits = []string{missed.SHA, queue.SHA, direct.SHA}
	g.facts[missed.SHA] = &missed
	g.checks[missed.SHA] = auditGreen()
	if err := f.m.ReconcileAudit(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if a = auditRead(t, f, missed.SHA); !reflect.DeepEqual(a.Flags, []string{"no_queue"}) || a.AlertedAt == nil {
		t.Fatalf("missed webhook not healed: %+v", a)
	}
	beforeRows, beforeEvents, beforeAlerts := auditCounts(t, f)
	if err := f.m.ReconcileAudit(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if rows, events, alerts := auditCounts(t, f); rows != beforeRows || events != beforeEvents || alerts != beforeAlerts {
		t.Fatal("reconciliation duplicated effects")
	}

	// Retain completed checks on the exact merge-group SHA even when GitHub
	// later cannot answer. These audit facts must not corrupt 848's rebuild.
	stored := queue
	stored.SHA = strings.Repeat("2", 40)
	stored.MergedBy = "admin"
	auditSend(t, f, "merge_group", "audit-group", map[string]any{"action": "checks_requested", "merge_group": map[string]string{"head_sha": stored.SHA, "base_sha": p.Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-group"}}, 204)
	for i, c := range auditGreen() {
		auditSend(t, f, "check_run", "audit-check-"+string(rune('a'+i)), map[string]any{"action": "completed", "check_run": map[string]string{"head_sha": stored.SHA, "name": c.Name, "status": c.Status, "conclusion": c.Conclusion}}, 204)
	}
	g.checksErr = errors.New("lookup unavailable")
	checkReads := g.checkReads
	auditSend(t, f, "pull_request", "audit-stored", auditPRBody(f, stored), 204)
	if g.checkReads != checkReads || len(auditRead(t, f, stored.SHA).Flags) != 0 {
		t.Fatal("stored exact-head checks/group provenance lost")
	}
	if err := f.m.Rebuild(t.Context(), f.person.TenantID); err != nil {
		t.Fatal("audit check facts broke projection rebuild", err)
	}

	var page AuditPage
	f.call(t, f.person, "GET", "/api/delivery/audit?project=AEON&flagged=true&limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Next == nil || len(page.Items[0].Flags) == 0 {
		t.Fatal("audit filter/pagination failed")
	}
	first := page.Items[0].SHA
	f.call(t, f.person, "GET", "/api/delivery/audit?project=AEON&flagged=true&limit=1&after="+*page.Next, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].SHA == first {
		t.Fatal("audit cursor repeated a row")
	}
	f.call(t, f.foreign, "GET", "/api/delivery/audit", nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("other tenant saw audit")
	}
	if err := db.InTenant(db.AllProjects(t.Context(), "audit RLS guard"), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_merge_audit WHERE tenant_id=$1`, f.person.TenantID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("audit RLS leaked tenant")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.ReconcileAudit(t.Context(), f.foreign.TenantID); !errors.Is(err, errNotConfigured) {
		t.Fatal("foreign tenant reconciliation allowed", err)
	}
	f.call(t, f.agent, "GET", "/api/delivery/audit", nil, 200, &page)
	denied := f.agent
	denied.Scopes = nil
	f.call(t, denied, "GET", "/api/delivery/audit", nil, 403, nil)
	f.call(t, f.person, "GET", "/api/delivery/audit?flagged=maybe", nil, 400, nil)
}

func TestMergeAuditPendingAlertsAndAtomicFailure(t *testing.T) {
	// Risks: reporting success after a partial write, losing a notification
	// when no lead exists, and sending to a lead after access was revoked.
	f := newFixture(t)
	f.build(t)
	f.review(t)
	g := &auditFake{fakeGitHub: f.gh, facts: map[string]*MergeFact{}, checks: map[string][]Check{}}
	f.m.github = g
	p := f.pull()
	p.Open = false
	p.Merged = true
	f.gh.pulls[7] = p
	pr := int64(7)
	fact := MergeFact{SHA: strings.Repeat("d", 40), Head: p.Head, Title: p.Title, Branch: p.Branch, PR: &pr, MergedBy: "admin", At: f.at}
	g.checksErr = errors.New("partial check lookup")
	auditSend(t, f, "pull_request", "audit-failure", auditPRBody(f, fact), 502)
	if rows, events, alerts := auditCounts(t, f); rows != 0 || events != 0 || alerts != 0 {
		t.Fatal("failed lookup committed audit")
	}
	g.checksErr = nil
	g.checks[fact.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "audit-failure", auditPRBody(f, fact), 204)
	if a := auditRead(t, f, fact.SHA); a.AlertedAt != nil || !reflect.DeepEqual(a.Flags, []string{"no_queue"}) {
		t.Fatal("missing lead marked notified")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state) VALUES($1,$2,$3,'paused')`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	if err := f.m.ReconcileAudit(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if rows, events, alerts := auditCounts(t, f); rows != 1 || events != 1 || alerts != 1 || auditRead(t, f, fact.SHA).AlertedAt == nil {
		t.Fatal("pending alert not retried exactly once")
	}

	// An inbox failure rolls back the audit row AND bypass event. Fail the
	// message insert with a transaction-local, reversible test trigger.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION audit_test_refuse_message() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected inbox failure'; END; $$; CREATE TRIGGER audit_test_refuse_message BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION audit_test_refuse_message()`)
		return err
	})
	next := fact
	next.SHA = strings.Repeat("e", 40)
	g.checks[next.SHA] = auditGreen()
	auditSend(t, f, "pull_request", "audit-inbox-fail", auditPRBody(f, next), 502)
	if rows, events, alerts := auditCounts(t, f); rows != 1 || events != 1 || alerts != 1 {
		t.Fatal("partial audit/inbox write survived failure")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER audit_test_refuse_message ON inbox_messages; DROP FUNCTION audit_test_refuse_message()`)
		return err
	})
	auditSend(t, f, "pull_request", "audit-inbox-fail", auditPRBody(f, next), 204)
	if rows, events, alerts := auditCounts(t, f); rows != 2 || events != 2 || alerts != 2 {
		t.Fatal("replay did not recover failed inbox acceptance")
	}
}
