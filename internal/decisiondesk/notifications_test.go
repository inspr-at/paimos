// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func (f *fixture) claim(t *testing.T, p tenant.Principal, item Item) (bool, error) {
	t.Helper()
	claimed := false
	err := db.InTenant(db.AllProjects(t.Context(), "desk claim test"), f.d.App, p.TenantID, func(tx pgx.Tx) error {
		var err error
		claimed, err = ClaimTx(t.Context(), tx, p, item)
		return err
	})
	return claimed, err
}

func TestConcurrentClaimsRetryRecipientAndRevocation(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "parked", []string{f.ticket})
	item := f.page(t, f.person, 100, nil).Items[0]
	if item.ID != q.ID || !item.Held {
		t.Fatal("fixture is not held")
	}
	start := make(chan struct{})
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; won, err := f.claim(t, f.person, item); results <- won; errs <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for won := range results {
		if won {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("replicas claimed %d pushes", winners)
	}
	if won, err := f.claim(t, f.person, item); err != nil || won {
		t.Fatalf("retry repeated push: %t %v", won, err)
	}
	if won, err := f.claim(t, f.reader, item); err != nil || !won {
		t.Fatalf("claim was not per recipient: %t %v", won, err)
	}
	// Capture a different, unclaimed source before revocation. An existing
	// claim must not make this authorization assertion pass by accident.
	f.ask(t, f.project, "paused", []string{f.ticket})
	readerPage := f.page(t, f.reader, 100, nil)
	var unclaimed Item
	for _, candidate := range readerPage.Items {
		if candidate.ID != item.ID {
			unclaimed = candidate
		}
	}
	if unclaimed.ID == "" {
		t.Fatal("missing fresh held source")
	}
	f.exec(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.reader.ID)
	f.reader.Roles = []string{"owner"}
	if won, err := f.claim(t, f.reader, unclaimed); err != nil || won {
		t.Fatalf("revoked recipient claimed push: %t %v", won, err)
	}
	err := db.InTenant(db.AllProjects(t.Context(), "desk retry test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		items, err := NoticesTx(t.Context(), tx, f.person, 10)
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].ID != unclaimed.ID {
			t.Fatal("claims starved or repeated a notice")
		}
		return FinishTx(t.Context(), tx, f.person, item, "failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if won, err := f.claim(t, f.person, item); err != nil || won {
		t.Fatal("ambiguous failure retried a push", err)
	}
	var state string
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT state FROM desk_notification_claims WHERE item_id=$1 AND recipient_id=$2`, item.ID, f.person.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("failed transport falsely reported success", state, err)
	}
}

func TestNotificationPointersAndNormalBriefingItem(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "carries_on", []string{f.ticket})
	page := f.page(t, f.person, 100, nil)
	if page.Counts.Open != 1 || page.Items[0].ID != q.ID || page.Items[0].Source != "/api/questions/"+q.ID {
		t.Fatal("ordinary question absent from briefing source")
	}
	err := db.InTenant(db.AllProjects(t.Context(), "desk normal-item test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		items, err := NoticesTx(t.Context(), tx, f.person, 100)
		if len(items) != 0 {
			t.Fatal("normal briefing item generated a push")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	item.Href = "/private-question-text"
	item.Title = "First line\nprivate-question-text"
	b, err := Payload(item, "AEON")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-question-text") {
		t.Fatal("push retained private question text")
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["url"] != "/decision-desk?item=q:"+q.ID || fields["body"] != "First line · AEON" || fields["title"] != "PAIMOS · Decision waiting" || fields["tag"] != "desk-question-"+q.ID {
		t.Fatalf("incorrect pointer: %s", b)
	}
	item.Kind = "doctrine"
	if _, err = Payload(item, "AEON"); err == nil {
		t.Fatal("doctrine toast also produced a push")
	}
}

func TestHeldRequestCanonicalizationDoesNotRepeatNotice(t *testing.T) {
	f := setup(t)
	// Authenticate the native inbox entry using an ephemeral test-only key.
	secret := rand.Text()
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + strings.Repeat("ab", 8)
	f.exec(t, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'held-fixture',$3,$4,$5,(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, f.agent.TenantID, f.agent.ID, prefix, hex.EncodeToString(sum[:]), []string{"inbox.send"})
	body, _ := json.Marshal(map[string]any{"to": f.person.ID, "body": "Private held request", "idempotency_key": "held-source", "is_action_request": true, "delivery_level": "simple"})
	r := httptest.NewRequest("POST", "/api/projects/"+f.project+"/messages", bytes.NewReader(body)).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_"+secret)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("held source %d: %s", w.Code, w.Body.String())
	}
	var held struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &held); err != nil {
		t.Fatal(err)
	}
	before := f.page(t, f.person, 100, nil)
	if len(before.Items) != 1 || before.Items[0].ID != held.ID || !before.Items[0].Held {
		t.Fatal("missing native held request")
	}
	var projectKey string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT key FROM nodes WHERE id=$1`, f.project).Scan(&projectKey); err != nil {
		t.Fatal(err)
	}
	if before.Items[0].Title != "Human request · "+projectKey {
		t.Fatalf("held request label lost project: %q", before.Items[0].Title)
	}
	// A project member cannot manage an inbox source. This counterpart is a
	// project admin, so decision authority is real while the browser notice
	// still belongs only to the held message's actual recipient.
	var admins int
	if err := f.d.Admin.QueryRow(t.Context(), `WITH updated AS (
		UPDATE role_bindings b SET role_id=r.id FROM roles r
		WHERE b.tenant_id=$1 AND b.principal_id=$2 AND b.scope_type='project' AND b.scope_id=$3
		AND r.tenant_id=b.tenant_id AND r.key='admin' RETURNING 1)
		SELECT count(*) FROM updated`, f.person.TenantID, f.reader.ID, f.project).Scan(&admins); err != nil || admins != 1 {
		t.Fatalf("project admin binding: %d %v", admins, err)
	}
	err := db.InTenant(db.AllProjects(t.Context(), "held browser recipient fixture"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		allowed, err := CurrentTx(t.Context(), tx, f.reader, before.Items[0])
		if err != nil {
			return err
		}
		if !allowed {
			t.Fatal("other-person control lacks decision authority")
		}
		_, own, err := BrowserCurrentTx(t.Context(), tx, f.person, before.Items[0])
		if err != nil {
			return err
		}
		_, other, err := BrowserCurrentTx(t.Context(), tx, f.reader, before.Items[0])
		if err != nil {
			return err
		}
		if !own || other {
			t.Fatalf("held recipient routing: own=%t other=%t", own, other)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if won, err := f.claim(t, f.person, before.Items[0]); err != nil || !won {
		t.Fatal("initial held notice missing", err)
	}
	q := f.askInput(t, f.project, questions.Input{RequestID: held.ID, Question: "Canonical held question", SourceRequestID: held.ID, Meanwhile: "parked", Options: []questions.Option{{ID: "yes", Title: "Yes", Answer: "Proceed"}}})
	after := f.page(t, f.person, 100, nil)
	if after.Counts.Open != 1 || after.Counts.Held != 1 || len(after.Items) != 1 || after.Items[0].ID != q.ID {
		t.Fatal("source and canonical question counted twice or lost held state")
	}
	if won, err := f.claim(t, f.person, after.Items[0]); err != nil || won {
		t.Fatal("canonicalizing a notified held request repeated its push", err)
	}
	if won, err := f.claim(t, f.reader, after.Items[0]); err != nil || !won {
		t.Fatal("canonicalization suppressed a fresh authorized recipient", err)
	}
	// A real answer and grace edit keep the work closed for every recipient.
	answered := f.decide(t, q, "Proceed")
	f.decide(t, answered, "Corrected answer")
	if got := f.page(t, f.person, 100, nil); got.Counts.Open != 0 {
		t.Fatal("answer resurrected linked held request")
	}
	if won, err := f.claim(t, f.person, after.Items[0]); err != nil || won {
		t.Fatal("decision/grace emitted notice", err)
	}
}

func TestApprovalExpiryWarningAndProjectReferenceAreNotHeld(t *testing.T) {
	f := setup(t)
	f.approval(t, f.project, time.Now().Add(time.Hour))
	near := f.approval(t, "", time.Now().Add(NearExpiry/2))
	f.approval(t, "", time.Now().Add(-time.Hour))
	var workOrder, run string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Waiting work',$2 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='work_order' RETURNING id::text`, f.person.TenantID, f.project).Scan(&workOrder); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'blocked')`, f.person.TenantID, workOrder, f.person.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status) VALUES($1,$2,$3,'waiting') RETURNING id::text`, f.person.TenantID, workOrder, f.agent.ID).Scan(&run)
	})
	if err != nil {
		t.Fatal(err)
	}
	linked := f.approval(t, "", time.Now().Add(time.Hour), run)
	page := f.page(t, f.person, 100, nil)
	if page.Counts.Open != 3 || page.Counts.Held != 1 {
		t.Fatal("a project reference or expired approval was marked held")
	}
	var scheduled []Item
	err = db.InTenant(db.AllProjects(t.Context(), "desk expiry test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		var err error
		scheduled, err = NoticesTx(t.Context(), tx, f.person, 100)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 2 || scheduled[0].ID != linked || scheduled[1].ID != near {
		t.Fatal("near-expiry policy admitted ordinary or expired approvals")
	}
	f.exec(t, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'denied')`, f.person.TenantID, near, f.person.ID)
	if won, err := f.claim(t, f.person, scheduled[1]); err != nil || won {
		t.Fatal("decided approval claimed expiry notification", err)
	}
	f.exec(t, `UPDATE work_orders SET status='done' WHERE node_id=$1`, workOrder)
	if won, err := f.claim(t, f.person, scheduled[0]); err != nil || won {
		t.Fatal("finished work still claimed a held approval", err)
	}
}

// Risk: a multiline question or a large project label exceeds Web Push limits
// or leaks the second line and caller-controlled navigation into the payload.
func TestDeskPayloadBoundsFirstLine(t *testing.T) {
	item := Item{Kind: "question", ID: "11111111-1111-4111-8111-111111111111", Revision: 1, Title: strings.Repeat("<", 1000) + "\nprivate second line", Href: "https://attacker.example/"}
	b, err := Payload(item, strings.Repeat("界", 1000))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 3000 || strings.Contains(string(b), "private") || strings.Contains(string(b), "attacker") {
		t.Fatalf("unbounded/private payload: %d bytes", len(b))
	}
	var fields map[string]string
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["body"] != strings.Repeat("<", 240)+" · "+strings.Repeat("界", 26) {
		t.Fatalf("wrong bounded body: %q", fields["body"])
	}
	for _, separator := range []string{"\r", "\n", "\u0085", "\u2028", "\u2029"} {
		item.Title = "Visible" + separator + "private second line"
		b, err = Payload(item, "P")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["body"] != "Visible · P" {
			t.Fatalf("second line escaped: %q", fields["body"])
		}
	}
}

const deskPushOrigin = "https://aeon.example"

type deskPushFixture struct {
	db              *dbtest.DB
	m               *phoneapprovals.Module
	mux             *http.ServeMux
	p, other, agent tenant.Principal
}

func deskPushFixtureFor(t *testing.T) *deskPushFixture {
	t.Helper()
	base := setup(t)
	dbtest.BindRole(t, base.d, base.person.TenantID, base.reader.ID, "admin")
	f := &deskPushFixture{db: base.d, mux: http.NewServeMux(), p: base.person, other: base.reader, agent: base.agent}
	f.m = phoneapprovals.New(f.db.App, nil, deskPushOrigin, make([]byte, 32), &webpush.Options{VAPIDPublicKey: "test-public"})
	f.m.Mount(f.mux)
	return f
}
func (f *deskPushFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *deskPushFixture) call(t *testing.T, p tenant.Principal, method, path string, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("Origin", origin)
	r = r.WithContext(authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.db.App))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func deskPushStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
}
func deskPushDigest(s string) string {
	b, _ := json.Marshal(s)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

type deskPushBarrier struct {
	PushSource
	scanned chan<- struct{}
}

func (s deskPushBarrier) NoticesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, visit func(string, string, int64, time.Time)) error {
	err := s.PushSource.NoticesTx(ctx, tx, p, visit)
	select {
	case s.scanned <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return err
}

// Risk: Desk text reaches another person or a device that did not opt in;
// replicas repeat claims, and bursts arriving on separate scans stack notices.
func TestDeskPushRecipientsBurstAndGone(t *testing.T) {
	for _, scenario := range []string{"single", "burst", "gone", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			f := deskPushFixtureFor(t)
			project, ticket, projectKey := f.deskProject(t)
			enabled := f.deskSubscription(t, f.p, "enabled", true)
			second := f.deskSubscription(t, f.p, "second", true)
			f.deskSubscription(t, f.p, "disabled", false)
			f.deskSubscription(t, f.other, "other-person", true)
			// Desk delivery must work with no passkey and disabled phone preferences.
			f.exec(t, `UPDATE phone_passkeys SET revoked_at=now() WHERE person_id=$1`, f.p.ID)
			type pushed struct {
				endpoint string
				fields   map[string]string
			}
			var pushes []pushed
			f.m.WithPushTransport(func(_ context.Context, b []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				fields := map[string]string{}
				if err := json.Unmarshal(b, &fields); err != nil {
					t.Fatal(err)
				}
				pushes = append(pushes, pushed{sub.Endpoint, fields})
				code := 201
				if scenario == "gone" && sub.Endpoint == enabled.Endpoint {
					code = 410
				}
				if scenario == "failed" {
					code = 503
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			q := f.deskQuestion(t, project, ticket, "First question line\nprivate second line")
			now := q.CreatedAt.Add(59 * time.Second)
			f.m.WithDeskClock(func() time.Time { return now })
			expectFailure := false
			dispatch := func() {
				t.Helper()
				err := f.m.Dispatch(t.Context())
				if expectFailure {
					code := "410"
					if scenario == "failed" {
						code = "503"
					}
					if err == nil || !strings.Contains(err.Error(), "desk push delivery failed (HTTP "+code+")") {
						t.Fatalf("wrong transport failure: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			dispatch()
			if len(pushes) != 0 {
				t.Fatal("notice escaped before the burst window closed")
			}
			count := 1
			if scenario == "burst" {
				f.deskQuestion(t, project, ticket, "Second question", q.CreatedAt.Add(10*time.Second))
				dispatch()
				f.deskQuestion(t, project, ticket, "Third question", q.CreatedAt.Add(20*time.Second))
				dispatch()
				if len(pushes) != 0 {
					t.Fatal("separate event scans escaped burst buffering")
				}
				count = 3
			}
			now = q.CreatedAt.Add(61 * time.Second)
			expectFailure = scenario == "gone" || scenario == "failed"
			dispatch()
			expectFailure = false
			if len(pushes) != 2 {
				t.Fatalf("got %d pushes, want one per opted-in device", len(pushes))
			}
			endpoints := map[string]bool{}
			for _, push := range pushes {
				endpoints[push.endpoint] = true
				if push.endpoint != enabled.Endpoint && push.endpoint != second.Endpoint {
					t.Fatal("push escaped the intended devices", push.endpoint)
				}
				if scenario == "burst" {
					if push.fields["title"] != "3 decisions waiting" || push.fields["tag"] != "desk-summary" || push.fields["url"] != "/decision-desk" {
						t.Fatalf("incorrect summary: %v", push.fields)
					}
				} else if push.fields["title"] != "PAIMOS · Decision waiting" || push.fields["body"] != "First question line · "+projectKey || push.fields["tag"] != "desk-question-"+q.ID || push.fields["url"] != "/decision-desk?item=q:"+q.ID {
					t.Fatalf("incorrect private payload: %v", push.fields)
				}
			}
			if !endpoints[enabled.Endpoint] || !endpoints[second.Endpoint] {
				t.Fatal("device fanout lost a device")
			}
			// A second committed hint for the same item still cannot send again.
			// Append refuses an event with no snapshot; the hint carries only the id and revision.
			if err := db.InTenant(db.AllProjects(t.Context(), "duplicate Desk event"), f.db.App, f.p.TenantID, func(tx pgx.Tx) error {
				_, err := events.Append(t.Context(), tx, f.agent, events.Change{Type: "question.asked", NodeID: &q.ID, After: map[string]any{"question_id": q.ID, "revision": q.Revision}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			dispatch()
			if len(pushes) != 2 {
				t.Fatal("duplicate event repeated a notice")
			}
			var claims, summaries, failed, other int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE summarised_at IS NOT NULL),count(*) FILTER(WHERE state='failed'),count(*) FILTER(WHERE recipient_id<>$1) FROM desk_notification_claims`, f.p.ID).Scan(&claims, &summaries, &failed, &other); err != nil {
				t.Fatal(err)
			}
			if claims != count || other != 0 {
				t.Fatalf("claims=%d other=%d", claims, other)
			}
			if scenario == "burst" && summaries != 2 {
				t.Fatalf("summary evidence=%d", summaries)
			}
			if (scenario == "gone" || scenario == "failed") && failed != 1 {
				t.Fatalf("failed delivery falsely reported sent: %d", failed)
			}
			if scenario == "gone" {
				var revoked, audits int
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM phone_push_subscriptions WHERE endpoint_hash=$1 AND revoked_at IS NOT NULL),(SELECT count(*) FROM events WHERE type='phone_approval.subscription_expired' AND after->>'subscription_id'=(SELECT id::text FROM phone_push_subscriptions WHERE endpoint_hash=$1))`, deskPushDigest(enabled.Endpoint)).Scan(&revoked, &audits); err != nil || revoked != 1 || audits != 1 {
					t.Fatalf("gone cleanup revoked=%d audits=%d: %v", revoked, audits, err)
				}
			}
		})
	}
}

// Risk: items older than one burst window, or held before Desk was turned on,
// each become their own notification. One pass must send one summary per
// device once three are due, and a pre-opt-in backlog must never be replayed
// item by item. One or two items created after opt-in stay individual.
func TestDeskPushDueWindowsAndOptInBacklog(t *testing.T) {
	for _, spec := range []struct {
		name       string
		items      int
		afterOptIn bool
		summary    bool
		holdLast   bool
	}{
		{name: "recovery", items: 3, afterOptIn: true, summary: true},
		{name: "pair", items: 2, afterOptIn: true, summary: false},
		{name: "opt-in", items: 3, afterOptIn: false, summary: true},
		{name: "opt-in-pair", items: 2, afterOptIn: false, summary: true},
		{name: "open-window", items: 3, afterOptIn: true, summary: false, holdLast: true},
	} {
		t.Run(spec.name, func(t *testing.T) {
			f := deskPushFixtureFor(t)
			project, ticket, _ := f.deskProject(t)
			f.deskSubscription(t, f.other, spec.name+"-other", true)
			subscribe := func(name string) webpush.Subscription {
				return f.deskSubscription(t, f.p, spec.name+"-"+name, true)
			}
			var enabled, second webpush.Subscription
			if spec.afterOptIn {
				enabled = subscribe("enabled")
				second = subscribe("second")
				f.deskSubscription(t, f.p, spec.name+"-disabled", false)
			}
			var base time.Time
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&base); err != nil {
				t.Fatal(err)
			}
			if spec.afterOptIn {
				base = base.Add(time.Minute)
			} else {
				base = base.Add(-3 * time.Hour)
			}
			qs := make([]questions.Question, spec.items)
			for i := range qs {
				qs[i] = f.deskQuestion(t, project, ticket, fmt.Sprintf("Held item %d", i+1), base.Add(time.Duration(i)*90*time.Second))
			}
			if !spec.afterOptIn {
				enabled = subscribe("enabled")
				second = subscribe("second")
				f.deskSubscription(t, f.p, spec.name+"-disabled", false)
				var opted, refreshed time.Time
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT desk_enabled_at FROM phone_push_subscriptions WHERE endpoint_hash=$1`, deskPushDigest(enabled.Endpoint)).Scan(&opted); err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"endpoint": enabled.Endpoint, "keys": enabled.Keys, "desk_enabled": true}
				deskPushStatus(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/subscriptions", body, deskPushOrigin), 200)
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT desk_enabled_at FROM phone_push_subscriptions WHERE endpoint_hash=$1`, deskPushDigest(enabled.Endpoint)).Scan(&refreshed); err != nil || !opted.Equal(refreshed) {
					t.Fatalf("refresh moved Desk opt-in: %v %v %v", opted, refreshed, err)
				}
			}
			type pushed struct {
				endpoint string
				fields   map[string]string
			}
			var pushes []pushed
			f.m.WithPushTransport(func(_ context.Context, b []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				fields := map[string]string{}
				if err := json.Unmarshal(b, &fields); err != nil {
					t.Fatal(err)
				}
				pushes = append(pushes, pushed{sub.Endpoint, fields})
				return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			now := qs[len(qs)-1].CreatedAt.Add(61 * time.Second)
			if spec.holdLast {
				now = qs[len(qs)-1].CreatedAt.Add(30 * time.Second)
			}
			f.m.WithDeskClock(func() time.Time { return now })
			dispatch := func() {
				t.Helper()
				if err := f.m.Dispatch(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			dispatch()
			wantDevices := map[string]bool{enabled.Endpoint: true, second.Endpoint: true}
			due := spec.items
			if spec.holdLast {
				due = spec.items - 1
			}
			check := func(from int, covered []questions.Question, summary bool, title string) {
				t.Helper()
				n := len(covered)
				if summary {
					n = 1
				}
				seen := map[string]map[string]int{}
				for _, push := range pushes[from:] {
					if !wantDevices[push.endpoint] {
						t.Fatalf("push escaped %s %v", push.endpoint, push.fields)
					}
					if seen[push.endpoint] == nil {
						seen[push.endpoint] = map[string]int{}
					}
					seen[push.endpoint][push.fields["tag"]]++
					if summary {
						if push.fields["tag"] != "desk-summary" || push.fields["title"] != title || push.fields["url"] != "/decision-desk" || push.fields["body"] != "Open Decision Desk to review." {
							t.Fatalf("backlog replayed: %v", push.fields)
						}
					} else if push.fields["tag"] == "desk-summary" || push.fields["title"] != "PAIMOS · Decision waiting" || !strings.HasPrefix(push.fields["url"], "/decision-desk?item=q:") {
						t.Fatalf("pair collapsed into a summary: %v", push.fields)
					}
				}
				if len(pushes)-from != len(wantDevices)*n || len(seen) != len(wantDevices) {
					t.Fatalf("pushes=%d devices=%d, want %d on each of %d", len(pushes)-from, len(seen), n, len(wantDevices))
				}
				for endpoint, tags := range seen {
					if summary {
						if tags["desk-summary"] != 1 || len(tags) != 1 {
							t.Fatalf("%s tags=%v", endpoint, tags)
						}
						continue
					}
					if len(tags) != len(covered) {
						t.Fatalf("%s tags=%v", endpoint, tags)
					}
					for _, q := range covered {
						if tags["desk-question-"+q.ID] != 1 {
							t.Fatalf("%s missing %s in %v", endpoint, q.ID, tags)
						}
					}
				}
			}
			title := fmt.Sprintf("%d decisions waiting", spec.items)
			covered := qs
			if spec.holdLast {
				covered = qs[:due]
			}
			check(0, covered, spec.summary, title)
			if spec.holdLast {
				now = qs[len(qs)-1].CreatedAt.Add(61 * time.Second)
				before := len(pushes)
				dispatch()
				check(before, qs[due:], false, "")
			}
			before := len(pushes)
			dispatch()
			if len(pushes) != before {
				t.Fatal("a second scan repeated a claimed notice")
			}
			var claims, summarised, other int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE summarised_at IS NOT NULL),count(*) FILTER(WHERE recipient_id<>$1) FROM desk_notification_claims`, f.p.ID).Scan(&claims, &summarised, &other); err != nil {
				t.Fatal(err)
			}
			wantSummarised := 0
			if spec.summary {
				wantSummarised = spec.items - 1
			}
			if claims != spec.items || summarised != wantSummarised || other != 0 {
				t.Fatalf("claims=%d summarised=%d other=%d", claims, summarised, other)
			}
		})
	}
}

func (f *deskPushFixture) deskSubscription(t *testing.T, p tenant.Principal, name string, enabled bool) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := webpush.Subscription{Endpoint: "https://web.push.apple.com/desk/" + name, Keys: webpush.Keys{P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}}
	body := map[string]any{"endpoint": sub.Endpoint, "keys": sub.Keys, "desk_enabled": enabled}
	deskPushStatus(t, f.call(t, p, "POST", "/api/me/phone-approvals/subscriptions", body, deskPushOrigin), 200)
	return sub
}

func (f *deskPushFixture) deskProject(t *testing.T) (string, string, string) {
	t.Helper()
	questions.New(f.db.App).Mount(f.mux)
	f.m.DeskPush = PushSource{}
	f.agent.Scopes = []string{"questions.ask", "questions.read"}
	var project, ticket, projectKey string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.p.TenantID, func(tx pgx.Tx) error {
		// Node keys must match nodes_key_check (PREFIX-N). The push body uses that key.
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Desk' FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='project' RETURNING id::text, key`, f.p.TenantID).Scan(&project, &projectKey); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Waiting work',$2 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='work' RETURNING id::text`, f.p.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state) VALUES($1,$2,$3,'paused')`, f.p.TenantID, project, f.p.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return project, ticket, projectKey
}

func (f *deskPushFixture) deskQuestion(t *testing.T, project, ticket, line string, at ...time.Time) questions.Question {
	t.Helper()
	var request string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT gen_random_uuid()::text`).Scan(&request); err != nil {
		t.Fatal(err)
	}
	in := questions.Input{RequestID: request, Question: line, Context: "private context", Findings: "private findings", Meanwhile: "parked", BlockedNodeIDs: []string{ticket}, Options: []questions.Option{{ID: "yes", Title: "Yes", Answer: "Private answer"}}}
	response := f.call(t, f.agent, "POST", "/api/projects/"+project+"/questions", in, "")
	deskPushStatus(t, response, 201)
	var q questions.Question
	if err := json.Unmarshal(response.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if len(at) > 0 {
		// All burst timestamps derive from the first source's database clock.
		err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE desk_questions SET created_at=$2 WHERE node_id=$1`, q.ID, at[0])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		q.CreatedAt = at[0]
	}
	return q
}

// Risk: opt-in bypasses ownership, provider validation, the cap, or disables
// phone registration's existing passkey requirement when no flag is supplied.
func TestDeskDeviceOptInOwnershipAndCap(t *testing.T) {
	f := deskPushFixtureFor(t)
	f.exec(t, `UPDATE phone_passkeys SET revoked_at=now() WHERE person_id=$1`, f.p.ID)
	sub := f.deskSubscription(t, f.p, "toggle", true)
	deskPushStatus(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/subscriptions", sub, deskPushOrigin), 409)
	settings := f.call(t, f.p, "GET", "/api/me/phone-approvals", nil, "")
	deskPushStatus(t, settings, 200)
	var out struct {
		Subscriptions []struct {
			ID           string `json:"id"`
			DeskEnabled  bool   `json:"desk_enabled"`
			EndpointHash string `json:"endpoint_hash"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(settings.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Subscriptions) != 1 || !out.Subscriptions[0].DeskEnabled || out.Subscriptions[0].EndpointHash != deskPushDigest(sub.Endpoint) || strings.Contains(settings.Body.String(), sub.Endpoint) {
		t.Fatal("incorrect device list", settings.Body.String())
	}
	id := out.Subscriptions[0].ID
	deskPushStatus(t, f.call(t, f.other, "DELETE", "/api/me/phone-approvals/subscriptions/"+id, nil, deskPushOrigin), 404)
	body := map[string]any{"endpoint": sub.Endpoint, "keys": sub.Keys, "desk_enabled": false}
	response := f.call(t, f.p, "POST", "/api/me/phone-approvals/subscriptions", body, deskPushOrigin)
	deskPushStatus(t, response, 200)
	if strings.Contains(response.Body.String(), `"desk_enabled":true`) {
		t.Fatal("disable reported true")
	}
	deskPushStatus(t, f.call(t, f.other, "POST", "/api/me/phone-approvals/subscriptions", body, deskPushOrigin), 409)
	deskPushStatus(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/subscriptions", body, "https://attacker.example"), 403)
	for n := 1; n < 8; n++ {
		f.deskSubscription(t, f.p, fmt.Sprintf("cap-%d", n), true)
	}
	body["endpoint"] = "https://web.push.apple.com/desk/ninth"
	deskPushStatus(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/subscriptions", body, deskPushOrigin), 409)
	body["endpoint"] = "https://attacker.example/desk"
	deskPushStatus(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/subscriptions", body, deskPushOrigin), 400)
	deskPushStatus(t, f.call(t, f.p, "DELETE", "/api/me/phone-approvals/subscriptions/"+id, nil, deskPushOrigin), 204)
	var revoked bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM phone_push_subscriptions WHERE id=$1`, id).Scan(&revoked); err != nil || !revoked {
		t.Fatal("device not removed", err)
	}
}

// Risk: question.asked never wakes the existing push listener. An injected
// recovery channel stays untouched; only the actual committed question can send.
func TestDeskQuestionCommittedEventWakesSender(t *testing.T) {
	f := deskPushFixtureFor(t)
	project, ticket, _ := f.deskProject(t)
	f.deskSubscription(t, f.p, "event", true)
	var now time.Time
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()+interval '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	f.m.WithDeskClock(func() time.Time { return now })
	sent := make(chan string, 8)
	f.m.WithPushTransport(func(_ context.Context, b []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var fields map[string]string
		if err := json.Unmarshal(b, &fields); err != nil {
			return nil, err
		}
		sent <- fields["url"]
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	scans, done := make(chan struct{}, 16), make(chan struct{})
	f.m.DeskPush = deskPushBarrier{scanned: scans}
	go func() { defer close(done); f.m.RunWithTicks(ctx, ticks) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("sender did not stop")
		}
	})
	for n := 0; n < 2; n++ {
		select {
		case <-scans:
		case <-time.After(10 * time.Second):
			t.Fatal("listener did not subscribe")
		}
	}
	q := f.deskQuestion(t, project, ticket, "Event question")
	select {
	case url := <-sent:
		if url != "/decision-desk?item=q:"+q.ID {
			t.Fatal("wrong item", url)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("question event did not wake sender")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sender did not stop")
	}
	select {
	case url := <-sent:
		t.Fatal("repeated event push", url)
	default:
	}
}
