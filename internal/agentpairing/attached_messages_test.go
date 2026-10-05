// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func qualifiedNotes() *attachedmsg.Service {
	return attachedmsg.New(attachedmsg.Options{Enabled: true, SingleInstance: true, Origin: origin, Capabilities: func(context.Context, pgx.Tx, string, string) (attachedmsg.HookCapability, error) {
		return attachedmsg.HookCapability{Verified: true, Version: "test-qualified-1"}, nil
	}})
}

type notesFixture struct {
	f         *fixture
	s         *attachedmsg.Service
	key       string
	in        attachwatch.DeviceRequest
	grant     attachedmsg.Grant
	recipient string
}

func readyNotes(t *testing.T) notesFixture {
	s := qualifiedNotes()
	f, key, in := watchFixture(t, s)
	// Status-only privacy remains unchanged when independently enabling messages.
	in.Snapshot.Mode = attachwatch.ModeLease
	in.Snapshot.Transcript = ""
	in.Snapshot.FileID = ""
	v := activateWatch(t, f, key, &in)
	in.MessageProtocol = attachedmsg.Protocol
	in.HookReleaseDigest = hash("verified release fixture")
	in.HookConfigDigest = hash("verified config fixture")
	in.QualifiedHarnessVersion = "test-qualified-1"
	in.Operation = "message_request"
	var c attachedmsg.Capability
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &c)
	if c.Grant == nil || c.Grant.Binding.SessionID != *v.SessionID || c.Grant.Binding.OwnerID != f.person {
		t.Fatal("missing exact grant")
	}
	path := "/api/agent-pairing/attach/" + in.RequestID + "/messages"
	f.call("POST", path+"/approve", map[string]string{"message_generation": c.Grant.Binding.Generation, "consent_digest": c.Grant.Digest}, true, "", 200)
	in.MessageGeneration = c.Grant.Binding.Generation
	in.MessageConsentDigest = c.Grant.Digest
	in.Operation = "message_activate"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	in.Operation = "message_observed"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	decodeResult(t, f.call("GET", path, nil, true, "", 200), &c)
	if !c.Ready || c.Grant == nil {
		t.Fatal("not ready after fresh observation")
	}
	var recipient string
	if e := f.db.Admin.QueryRow(t.Context(), `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1`, *v.SessionID).Scan(&recipient); e != nil {
		t.Fatal(e)
	}
	return notesFixture{f, s, key, in, *c.Grant, recipient}
}
func (n notesFixture) body(key, body string, compat bool) map[string]any {
	v := map[string]any{"recipient_session_id": n.grant.Binding.SessionID, "recipient_message_generation": n.grant.Binding.Generation, "body": body, "idempotency_key": key}
	if compat {
		v["to"] = n.recipient
	} else {
		v["recipient_principal_id"] = n.recipient
	}
	return v
}
func (n notesFixture) path(compat bool) string {
	if compat {
		return "/api/projects/" + n.grant.Binding.ProjectID + "/messages"
	}
	return "/api/inbox/messages"
}
func assertNoCanary(t *testing.T, f *fixture, canary string) {
	t.Helper()
	rows, e := f.db.Admin.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public'`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		var n int
		e = f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` t WHERE to_jsonb(t)::text LIKE $1`, "%"+canary+"%").Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n != 0 {
			t.Fatalf("payload persisted in %s", table)
		}
	}
}
func TestAttachedNoteCanariesAndIdempotency(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, compat := range []bool{false, true} {
		canary := "aeon392-body-" + attachedmsg.UUID()
		key := attachedmsg.UUID()
		body := n.body(key, canary, compat)
		res := f.call("POST", n.path(compat), body, true, "", 201)
		if res.Header().Get("Cache-Control") != "no-store" || strings.Contains(res.Body.String(), canary) {
			t.Fatal("body in response/cache")
		}
		var msg inbox.Message
		decodeResult(t, res, &msg)
		if msg.ContentMode != attachedmsg.Volatile || msg.Body != attachedmsg.Placeholder || msg.RecipientMessageGeneration == nil {
			t.Fatal("missing volatile projection")
		}
		repeated := f.call("POST", n.path(compat), body, true, "", 201)
		var again inbox.Message
		decodeResult(t, repeated, &again)
		if again.ID != msg.ID {
			t.Fatal("duplicate note")
		}
		body["body"] = canary + " changed"
		f.call("POST", n.path(compat), body, true, "", 409)
		f.call("POST", "/api/inbox/messages/"+msg.ID+"/ack", nil, true, "", 404)
		status := f.call("GET", "/api/inbox/message-status?ids="+msg.ID, nil, true, "", 200)
		if !strings.Contains(status.Body.String(), `"outcome":"queued"`) || strings.Contains(status.Body.String(), `"status":"read"`) {
			t.Fatal("dishonest status")
		}
		assertNoCanary(t, f, canary)
		if strings.Contains(logs.String(), canary) || strings.Contains(res.Header().Get("X-Request-ID"), canary) {
			t.Fatal("payload in logging/trace capture")
		}
	}
	// A post-reservation SQL failure must roll back metadata and forget text.
	_, e := f.db.Admin.Exec(t.Context(), `CREATE FUNCTION test_reject_note_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture receipt failure' USING ERRCODE='XX000'; END $$; CREATE TRIGGER test_reject_note_receipt BEFORE INSERT ON inbox_receipts FOR EACH ROW EXECUTE FUNCTION test_reject_note_receipt()`)
	if e != nil {
		t.Fatal(e)
	}
	canary := "aeon392-rollback-" + attachedmsg.UUID()
	f.call("POST", n.path(false), n.body("rollback", canary, false), true, "", 500)
	assertNoCanary(t, f, canary)
	if strings.Contains(logs.String(), canary) {
		t.Fatal("failed payload in logs")
	}
	_, e = f.db.Admin.Exec(t.Context(), `DROP TRIGGER test_reject_note_receipt ON inbox_receipts; DROP FUNCTION test_reject_note_receipt()`)
	if e != nil {
		t.Fatal(e)
	}
	// The failed reservation did not consume the remaining token.
	f.call("POST", n.path(false), n.body("after-rollback", "accepted", false), true, "", 201)
}
func TestAttachedConsentAndTupleNegatives(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	path := "/api/agent-pairing/attach/" + n.in.RequestID + "/messages"
	f.call("POST", path+"/approve", map[string]string{"message_generation": n.grant.Binding.Generation, "consent_digest": n.grant.Digest}, true, "", 409)
	for _, compat := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			change func(map[string]any)
		}{
			{"stale-generation", func(v map[string]any) { v["recipient_message_generation"] = attachedmsg.UUID() }},
			{"missing-generation", func(v map[string]any) { delete(v, "recipient_message_generation") }},
			{"omitted-session", func(v map[string]any) { delete(v, "recipient_session_id") }},
		} {
			t.Run(fmt.Sprintf("%t-%s", compat, tc.name), func(t *testing.T) {
				b := n.body(attachedmsg.UUID(), "rejected", compat)
				tc.change(b)
				f.call("POST", n.path(compat), b, true, "", 409)
			})
		}
		// Browser session evidence and exact Origin are independently required.
		for _, badOrigin := range []string{"", "https://evil.test"} {
			r := f.request("POST", n.path(compat), n.body(attachedmsg.UUID(), "csrf", compat), true, "")
			r.Header.Set("Origin", badOrigin)
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("csrf status %d", w.Code)
			}
		}
	}
	for _, field := range []string{"delivery_level", "is_action_request", "thread_id", "reply_to"} {
		b := n.body(attachedmsg.UUID(), "no control", true)
		switch field {
		case "delivery_level":
			b[field] = "steer"
		case "is_action_request":
			b[field] = true
		case "thread_id":
			b[field] = "chosen"
		case "reply_to":
			b[field] = attachedmsg.UUID()
		}
		f.call("POST", n.path(true), b, true, "", 400)
	}
	for _, field := range []string{"MessageGeneration", "MessageConsentDigest", "HookReleaseDigest", "HookConfigDigest", "QualifiedHarnessVersion"} {
		in := n.in
		switch field {
		case "MessageGeneration":
			in.MessageGeneration = attachedmsg.UUID()
		case "MessageConsentDigest":
			in.MessageConsentDigest = hash("wrong")
		case "HookReleaseDigest":
			in.HookReleaseDigest = hash("changed")
		case "HookConfigDigest":
			in.HookConfigDigest = hash("changed")
		case "QualifiedHarnessVersion":
			in.QualifiedHarnessVersion = "changed"
		}
		f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 409)
	}
	// Revoke leaves watching active and does not renew its lease.
	var before, after string
	f.db.Admin.QueryRow(t.Context(), `SELECT lease_until::text FROM harness_attach_requests WHERE id=$1`, n.in.RequestID).Scan(&before)
	f.call("POST", path+"/revoke", nil, true, "", 200)
	f.db.Admin.QueryRow(t.Context(), `SELECT lease_until::text FROM harness_attach_requests WHERE id=$1 AND state='active'`, n.in.RequestID).Scan(&after)
	if before != after || after == "" {
		t.Fatal("messaging changed watching")
	}
	f.call("POST", n.path(false), n.body("revoked", "not accepted", false), true, "", 409)
	n.in.Operation = "message_request"
	n.in.MessageGeneration = ""
	n.in.MessageConsentDigest = ""
	var c attachedmsg.Capability
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", n.in, false, n.key, 200), &c)
	if c.Grant.Binding.Generation == n.grant.Binding.Generation {
		t.Fatal("generation inherited")
	}
	f.call("POST", path+"/approve", map[string]string{"message_generation": n.grant.Binding.Generation, "consent_digest": n.grant.Digest}, true, "", 409)
}
func TestAttachedConcurrentQuotas(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	results := concurrentNoteRequests(t, n, 12, func(i int) string { return fmt.Sprint("race-", i) }, func(int) string { return "bounded" })
	codes := make(chan int, len(results))
	for _, w := range results {
		if w.Code == 429 && w.Header().Get("Retry-After") == "" {
			codes <- 0
		} else {
			codes <- w.Code
		}
	}
	close(codes)
	accepted := 0
	for code := range codes {
		if code == 201 {
			accepted++
		} else if code != 429 {
			t.Fatalf("unexpected concurrent status %d", code)
		}
	}
	if accepted != 3 {
		t.Fatalf("burst accepted %d want 3", accepted)
	}
	_, e := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET message_tokens=3 WHERE id=$1`, n.in.RequestID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		f.call("POST", n.path(false), n.body(fmt.Sprint("queue-", i), "bounded", false), true, "", 201)
	}
	f.call("POST", n.path(false), n.body("queue-overflow", "bounded", false), true, "", 429)
}

func TestAttachedSenderAndTenantPolicy(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	owner := tenant.Principal{ID: f.person, TenantID: f.tenantID, Kind: tenant.Person, BrowserSession: true, Name: "Owner"}
	var adminID, agentID string
	for _, v := range []struct {
		kind string
		id   *string
	}{{"person", &adminID}, {"agent", &agentID}} {
		e := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,$2,'Markus','{admin}') RETURNING id::text`, f.tenantID, v.kind).Scan(v.id)
		if e != nil {
			t.Fatal(e)
		}
		dbtest.BindRole(t, f.db, f.tenantID, *v.id, "admin")
	}
	check := func(p tenant.Principal, send attachedmsg.Send) (attachedmsg.Acceptance, error) {
		r := httptest.NewRequest("POST", origin+"/api/inbox/messages", nil)
		r.Header.Set("Origin", origin)
		ctx := tenant.WithPrincipal(r.Context(), p)
		r = r.WithContext(ctx)
		ctx = attachedmsg.BrowserContext(r, origin, p)
		var out attachedmsg.Acceptance
		e := db.InTenant(ctx, f.db.App, p.TenantID, func(tx pgx.Tx) error {
			if e := attachedmsg.Lock(ctx, tx); e != nil {
				return e
			}
			var e error
			out, e = n.s.Authorize(ctx, tx, p, n.in.RequestID, send)
			return e
		})
		return out, e
	}
	send := attachedmsg.Send{Recipient: n.recipient, Project: n.grant.Binding.ProjectID, Session: &n.grant.Binding.SessionID, Generation: n.grant.Binding.Generation, Body: "policy-canary"}
	admin := owner
	admin.ID = adminID
	if _, e := check(admin, send); e == nil {
		t.Fatal("another person/admin injected")
	}
	agent := tenant.Principal{ID: agentID, TenantID: f.tenantID, Kind: tenant.Agent, Name: "Markus", Scopes: []string{"*", "inbox.send"}, KeyCreatorID: f.person}
	a, e := check(agent, send)
	if e != nil || a.Mode != attachedmsg.Notification || a.GrantID != nil {
		t.Fatalf("agent promoted or denied notification: %v mode %s", e, a.Mode)
	}
	forged := agent
	forged.Kind = tenant.Person
	forged.BrowserSession = false
	if _, e = check(forged, send); e == nil {
		t.Fatal("forged person injected")
	}
	for _, field := range []string{"project", "principal", "session"} {
		bad := send
		switch field {
		case "project":
			bad.Project = attachedmsg.UUID()
		case "principal":
			bad.Recipient = adminID
		case "session":
			sid := attachedmsg.UUID()
			bad.Session = &sid
		}
		if _, e = check(owner, bad); e == nil {
			t.Fatalf("wrong %s accepted", field)
		}
	}
	_, e = f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests SET approved_by=$2 WHERE computer_id=$1`, n.in.ComputerID, adminID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = check(owner, send); e == nil {
		t.Fatal("changed owner retained authority")
	}
	_, e = f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests SET approved_by=$2 WHERE computer_id=$1`, n.in.ComputerID, f.person)
	if e != nil {
		t.Fatal(e)
	}
	// A current authorization check catches membership loss, despite the cookie.
	dbtest.BindRole(t, f.db, f.tenantID, adminID, "owner")
	_, e = f.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = check(owner, send); e == nil {
		t.Fatal("stale membership retained authority")
	}
}

func otherPersonCookie(t *testing.T, f *fixture) (string, *http.Cookie) {
	t.Helper()
	var identity, id string
	email := attachedmsg.UUID() + "@example.test"
	e := f.db.Admin.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,email,display_name) VALUES('attached-test',$1,$1,'Other owner') RETURNING id::text`, email).Scan(&identity)
	if e != nil {
		t.Fatal(e)
	}
	e = f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,identity_id,name,email,roles) VALUES($1,'person',$2,'Other admin',$3,'{admin}') RETURNING id::text`, f.tenantID, identity, email).Scan(&id)
	if e != nil {
		t.Fatal(e)
	}
	dbtest.BindRole(t, f.db, f.tenantID, id, "admin")
	res := f.call("POST", "/api/auth/dev-login", map[string]string{"email": email}, false, "", 200)
	if len(res.Result().Cookies()) == 0 {
		t.Fatal("no other cookie")
	}
	return id, res.Result().Cookies()[0]
}
func TestAttachedAPIIdentityAndLegacyConsumers(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	var created struct {
		Token string `json:"token"`
		ID    string `json:"principal_id"`
	}
	decodeResult(t, f.call("POST", "/api/agent-keys", map[string]any{"name": "Markus", "scopes": []string{"inbox.send", "inbox.read", "nodes.read", "harness.read"}}, true, "", 201), &created)
	for _, compat := range []bool{false, true} {
		b := n.body(attachedmsg.UUID(), "agent-canary-"+attachedmsg.UUID(), compat)
		delete(b, "recipient_message_generation")
		r := f.request("POST", n.path(compat), b, false, created.Token)
		r.Header.Set("X-Sender-Kind", "person")
		r.Header.Set("X-Sender-ID", f.person)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != 201 {
			t.Fatalf("agent notification status %d", w.Code)
		}
		var msg inbox.Message
		decodeResult(t, w, &msg)
		if msg.ContentMode != attachedmsg.Notification || msg.MessageGrantID != nil {
			t.Fatal("agent injected")
		}
		assertNoCanary(t, f, b["body"].(string))
		// Inspect every reservation, regardless of tuple; a wrong-tuple Take alone
		// cannot catch accidentally retaining agent text. No concurrent store users.
		entries := reflect.ValueOf(n.s).Elem().FieldByName("entries").MapRange()
		for entries.Next() {
			if entries.Value().Elem().FieldByName("body").String() != "" {
				t.Fatal("agent body retained in volatile store")
			}
		}
		notice := attachedmsg.Binding{TenantID: f.tenantID, ProjectID: n.grant.Binding.ProjectID, SessionID: n.grant.Binding.SessionID, ComputerID: n.in.ComputerID, OwnerID: f.person, AttachRequestID: n.in.RequestID, ServiceEpoch: n.s.Epoch()}
		if e := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
			body, ok := n.s.Take(notice, "", msg.ID, attachedmsg.TakeTransaction{Context: t.Context(), Tx: tx})
			if body != "" || ok {
				t.Fatal("notification's own tuple returned a payload")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if _, e := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET message_notice_at=NULL,message_tokens=3 WHERE id=$1`, n.in.RequestID); e != nil {
			t.Fatal(e)
		}
	}
	b := n.body("owner-read-fence", "private-owner-canary", true)
	var msg inbox.Message
	decodeResult(t, f.call("POST", n.path(true), b, true, "", 201), &msg)
	ownerHistory := f.call("GET", n.path(true)+"?session="+n.grant.Binding.SessionID, nil, true, "", 200)
	if !strings.Contains(ownerHistory.Body.String(), msg.ID) || !strings.Contains(ownerHistory.Body.String(), attachedmsg.Placeholder) {
		t.Fatal("owner metadata history missing")
	}
	ownerCookie := f.cookie
	_, otherCookie := otherPersonCookie(t, f)
	f.cookie = otherCookie
	f.call("POST", n.path(false), n.body("admin-denied", "denied", false), true, "", 403)
	f.call("POST", n.path(true), n.body("admin-denied", "denied", true), true, "", 403)
	f.call("POST", "/api/agent-pairing/attach/"+n.in.RequestID+"/messages/approve", map[string]string{"message_generation": n.grant.Binding.Generation, "consent_digest": n.grant.Digest}, true, "", 403)
	history := f.call("GET", n.path(true)+"?session="+n.grant.Binding.SessionID, nil, true, "", 200)
	if strings.Contains(history.Body.String(), msg.ID) {
		t.Fatal("other admin saw owner history")
	}
	f.cookie = ownerCookie
	// Exercise the inbox layer directly as the recipient: the pairing HTTP fence
	// is stricter, but cannot be the only thing protecting legacy consumers.
	mux := http.NewServeMux()
	inbox.New(f.db.App, n.s).Mount(mux)
	compat, e := inbox.NewMessaging(f.db.App, make([]byte, 32), inbox.WithAttachedMessages(n.s))
	if e != nil {
		t.Fatal(e)
	}
	compat.Mount(mux)
	p := tenant.Principal{ID: n.recipient, TenantID: f.tenantID, Kind: tenant.Agent}
	for _, path := range []string{"/api/inbox/messages?session=" + n.grant.Binding.SessionID, n.path(true) + "/listen?session=" + n.grant.Binding.SessionID} {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), msg.ID) {
			t.Fatalf("legacy read status %d", w.Code)
		}
	}
	for _, path := range []string{"/api/inbox/messages/" + msg.ID + "/ack", n.path(true) + "/" + msg.ID + "/ack"} {
		r := httptest.NewRequest("POST", path, nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("legacy ack status %d", w.Code)
		}
	}
	// The database guard is a final backstop, including an old binary's UPDATE.
	e = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE inbox_messages SET acked_at=clock_timestamp() WHERE id=$1`, msg.ID)
		return e
	})
	if e == nil {
		t.Fatal("legacy DB ack advanced")
	}
	assertNoCanary(t, f, "private-owner-canary")
}
func TestAttachedRestartRLSAndQuotaCounters(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	var msg inbox.Message
	body := n.body("restart-key", "restart-canary-"+attachedmsg.UUID(), false)
	decodeResult(t, f.call("POST", n.path(false), body, true, "", 201), &msg)
	other := attachedmsg.UUID()
	if _, e := f.db.Admin.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$1::uuid::text,'Other')`, other); e != nil {
		t.Fatal(e)
	}
	e := db.InTenant(dbtest.Seed(t.Context()), f.db.App, other, func(tx pgx.Tx) error {
		for _, table := range []string{"attached_message_grants", "inbox_messages", "inbox_compat_messages"} {
			var count int
			if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&count); e != nil {
				return e
			}
			if count != 0 {
				t.Fatalf("cross-tenant %s leak", table)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	replacement := qualifiedNotes()
	if e = replacement.Sweep(t.Context(), f.db.App, f.tenantID); e != nil {
		t.Fatal(e)
	}
	var state, reason string
	if e = f.db.Admin.QueryRow(t.Context(), `SELECT m.attached_outcome,r.failure_reason FROM inbox_messages m JOIN inbox_receipts r ON r.message_id=m.id AND r.tenant_id=m.tenant_id WHERE m.id=$1`, msg.ID).Scan(&state, &reason); e != nil || state != "not_delivered" || reason != "content_lost" {
		t.Fatalf("restart outcome %s/%s: %v", state, reason, e)
	}
	// The old process still has the body: Take must see the sibling's terminal
	// row before this process gets a chance to run its own sweeper.
	assertTake := func(tx pgx.Tx) error {
		body, ok := n.s.Take(n.grant.Binding, n.grant.ID, msg.ID, attachedmsg.TakeTransaction{Context: t.Context(), Tx: tx})
		if ok || body != "" {
			t.Fatal("content_lost body released by old process")
		}
		return nil
	}
	if e = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, assertTake); e != nil {
		t.Fatal(e)
	}
	// Even the prior process must purge/deny a terminalized payload before offer.
	if e = n.s.Sweep(t.Context(), f.db.App, f.tenantID); e != nil {
		t.Fatal(e)
	}
	// Transactional owner/computer quota applies independently of session tokens.
	if _, e = f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_computers SET message_window_count=30,message_window_at=clock_timestamp() WHERE id=$1`, n.in.ComputerID); e != nil {
		t.Fatal(e)
	}
	f.call("POST", n.path(false), n.body("owner-quota", "limited", false), true, "", 429)
	assertNoCanary(t, f, body["body"].(string))
}
func TestAttachedConsentDowngradeAndDefaultCapability(t *testing.T) {
	s := qualifiedNotes()
	f, key, in := watchFixture(t, s)
	activateWatch(t, f, key, &in)
	in.Operation = "message_request"
	in.MessageProtocol = attachedmsg.Protocol
	in.HookReleaseDigest = hash("release")
	in.HookConfigDigest = hash("config")
	in.QualifiedHarnessVersion = "test-qualified-1"
	var c attachedmsg.Capability
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &c)
	path := "/api/agent-pairing/attach/" + in.RequestID + "/messages/approve"
	for _, digest := range []string{"", hash("wrong")} {
		f.call("POST", path, map[string]string{"message_generation": c.Grant.Binding.Generation, "consent_digest": digest}, true, "", map[bool]int{true: 400, false: 409}[digest == ""])
	}
	// Changing the person's saved consent policy cannot be silently downgraded.
	if _, e := f.db.Admin.Exec(t.Context(), `INSERT INTO person_watch_security(tenant_id,person_id,consent_mode) VALUES($1,$2,'local_auth')`, f.tenantID, f.person); e != nil {
		t.Fatal(e)
	}
	f.call("POST", path, map[string]string{"message_generation": c.Grant.Binding.Generation, "consent_digest": c.Grant.Digest}, true, "", 409)
	// No 391 migration or qualified native evidence: the production adapter must
	// return repair/qualification blockers rather than trusting client pins.
	f.pairing.SetAttachedMessages(attachedmsg.New(attachedmsg.Options{Enabled: true, SingleInstance: true, Origin: origin}))
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	if _, e := f.db.Admin.Exec(t.Context(), `ALTER TABLE agent_pairing_computers ADD COLUMN IF NOT EXISTS hook_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_computers SET hook_capabilities='[{"harness":"codex","version":"test-qualified-1","os":"darwin","verified":false,"blocker":"project_override"}]'::jsonb WHERE id=$1`, in.ComputerID); e != nil {
		t.Fatal(e)
	}
	var blocked attachedmsg.Capability
	decodeResult(t, f.call("GET", strings.TrimSuffix(path, "/approve"), nil, true, "", 200), &blocked)
	if blocked.Blocker != "project_override" {
		t.Fatal("specific capability blocker lost")
	}
	f.pairing.SetAttachedMessages(nil)
	var disabled attachedmsg.Capability
	decodeResult(t, f.call("GET", strings.TrimSuffix(path, "/approve"), nil, true, "", 200), &disabled)
	if disabled.Ready || disabled.Blocker != "feature_disabled" {
		t.Fatal("default enabled")
	}
}

type noteQueryCapture struct {
	mu   sync.Mutex
	data strings.Builder
}

func (c *noteQueryCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.WriteString(data.SQL)
	fmt.Fprint(&c.data, data.Args)
	return ctx
}
func (*noteQueryCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestAttachedSQLTraceCanary(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	capture := &noteQueryCapture{}
	cfg := f.db.App.Config()
	cfg.ConnConfig.Tracer = capture
	pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	mux := http.NewServeMux()
	inbox.New(pool, n.s).Mount(mux)
	canary := "sql-trace-canary-" + attachedmsg.UUID()
	r := f.request("POST", n.path(false), n.body("trace", canary, false), true, "")
	r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{ID: f.person, TenantID: f.tenantID, Kind: tenant.Person, BrowserSession: true, Name: "Owner"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("trace send status %d", w.Code)
	}
	if strings.Contains(capture.data.String(), canary) {
		t.Fatal("raw body entered SQL tracing")
	}
	assertNoCanary(t, f, canary)
}

func TestAttachedStrictLocalConsentAndNoLeaseRenewal(t *testing.T) {
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(signer.Curve, signer.X, signer.Y))
	service := qualifiedNotes()
	f, key, in := watchFixtureWithHarness(t, public, "codex", service)
	in.Snapshot.Platform = "darwin"
	v := requestWatch(t, f, key, in)
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200), &v)
	in.Operation, in.Sequence, in.Digest, in.ConsentDigest = "poll", 1, v.Digest, v.ConsentDigest
	in.LocalAuthNonce = v.LocalAuthNonce
	in.LocalAuthSignature = signWatchConsent(t, signer, v.ConsentDigest, v.LocalAuthNonce, in.Snapshot)
	watchSignature := in.LocalAuthSignature
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	var before string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT lease_until::text FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	in.Operation, in.MessageProtocol = "message_request", attachedmsg.Protocol
	in.LocalAuthNonce, in.LocalAuthSignature = "", ""
	in.HookReleaseDigest, in.HookConfigDigest, in.QualifiedHarnessVersion = hash("release"), hash("config"), "test-qualified-1"
	var c attachedmsg.Capability
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &c)
	if c.Grant.Binding.ConsentPolicy != attachwatch.ConsentLocalAuth {
		t.Fatal("unsaved pinned-key policy downgraded")
	}
	path := "/api/agent-pairing/attach/" + in.RequestID + "/messages"
	decodeResult(t, f.call("POST", path+"/approve", map[string]string{"message_generation": c.Grant.Binding.Generation, "consent_digest": c.Grant.Digest}, true, "", 200), &c)
	grant := *c.Grant
	if !attachwatch.LocalAuthNonceValid(grant.LocalAuthNonce) || grant.LocalAuthNonce == v.LocalAuthNonce {
		t.Fatal("independent messaging nonce missing")
	}
	in.Operation, in.MessageGeneration, in.MessageConsentDigest = "message_activate", grant.Binding.Generation, grant.Digest
	// Correct failure reasons ensure these cannot pass because of unrelated auth.
	requireRefusal := func(w *httptest.ResponseRecorder, code string) {
		t.Helper()
		var out struct{ Code string }
		decodeResult(t, w, &out)
		if out.Code != code {
			t.Fatalf("wrong refusal: %s", out.Code)
		}
	}
	in.LocalConfirmed = true
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "local_auth_proof_required")
	in.LocalConfirmed = false
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	sign := func(k *ecdsa.PrivateKey, digest, nonce, reason string) string {
		t.Helper()
		raw, e := ecdsa.SignASN1(rand.Reader, k, attachedmsg.LocalConsentHash(digest, nonce, reason))
		if e != nil {
			t.Fatal(e)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	in.MessageLocalAuthNonce = grant.LocalAuthNonce
	wrong, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	in.MessageLocalAuthSignature = sign(wrong, grant.Digest, grant.LocalAuthNonce, attachedmsg.LocalConsentReason(grant))
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	// Even a watch-domain signature over the messaging digest/challenge fails.
	in.MessageLocalAuthSignature = signWatchConsent(t, signer, grant.Digest, grant.LocalAuthNonce, in.Snapshot)
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	in.MessageLocalAuthSignature = watchSignature
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	in.MessageLocalAuthNonce = hash("other nonce")
	in.MessageLocalAuthSignature = sign(signer, grant.Digest, in.MessageLocalAuthNonce, attachedmsg.LocalConsentReason(grant))
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	in.MessageLocalAuthNonce = grant.LocalAuthNonce
	in.MessageLocalAuthSignature = sign(signer, grant.Digest, grant.LocalAuthNonce, "Allow watching")
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	in.MessageLocalAuthSignature = sign(signer, grant.Digest, grant.LocalAuthNonce, attachedmsg.LocalConsentReason(grant))
	var active attachedmsg.Capability
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &active)
	c = active
	if c.Grant.State != "active" || c.Grant.LocalAuthNonce != "" {
		t.Fatal("challenge not consumed atomically")
	}
	var consumed bool
	if e := f.db.Admin.QueryRow(t.Context(), `SELECT local_auth_nonce IS NULL FROM attached_message_grants WHERE id=$1`, grant.ID).Scan(&consumed); e != nil || !consumed {
		t.Fatal("challenge retained", e)
	}
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 409), "message_consent_mismatch")
	in.Operation, in.MessageLocalAuthNonce, in.MessageLocalAuthSignature = "message_observed", "", ""
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	var after string
	if e := f.db.Admin.QueryRow(t.Context(), `SELECT lease_until::text FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&after); e != nil || after != before {
		t.Fatal("message operation renewed lease", e)
	}
	// Re-enabling the same live session creates fresh generation and challenge.
	f.call("POST", path+"/revoke", nil, true, "", 200)
	in.Operation, in.MessageGeneration, in.MessageConsentDigest = "message_request", "", ""
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &c)
	decodeResult(t, f.call("POST", path+"/approve", map[string]string{"message_generation": c.Grant.Binding.Generation, "consent_digest": c.Grant.Digest}, true, "", 200), &c)
	in.Operation, in.MessageGeneration, in.MessageConsentDigest = "message_activate", c.Grant.Binding.Generation, c.Grant.Digest
	in.MessageLocalAuthNonce = grant.LocalAuthNonce
	in.MessageLocalAuthSignature = sign(signer, grant.Digest, grant.LocalAuthNonce, attachedmsg.LocalConsentReason(grant))
	requireRefusal(f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "message_local_auth_proof_required")
	in.MessageLocalAuthNonce, in.MessageLocalAuthSignature = "", ""
	in.Operation = "detach"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	decodeResult(t, f.call("GET", path, nil, true, "", 200), &c)
	if c.Ready || c.Blocker != "attachment_offline" {
		t.Fatal("detached capability not blocked")
	}
}

func TestAttachedMultiTabIdempotency(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	// Both contenders use the same compat route/idempotency namespace.
	h, b := fencedNotesHandler(t, n, 2, true)
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, n.f.request("POST", n.path(true), n.body("same-tab-key", fmt.Sprint("tab body ", i), true), true, ""))
			results <- w
		}(i)
	}
	awaitNoteFences(t, b, 2)
	close(b.release)
	wg.Wait()
	close(results)
	accepted, conflict := 0, 0
	for w := range results {
		switch w.Code {
		case 201:
			accepted++
		case 409:
			conflict++
		default:
			t.Fatalf("unexpected tab result %d", w.Code)
		}
	}
	if accepted != 1 || conflict != 1 {
		t.Fatalf("tab race accepted=%d conflicted=%d", accepted, conflict)
	}
	var count int
	if e := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE content_mode='attached_volatile'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("duplicate row count %d: %v", count, e)
	}
}

func TestAttachedInactiveRoutingPreservesInbox(t *testing.T) {
	for _, mode := range []string{"nil", "disabled", "unqualified", "detached", "expired"} {
		t.Run(mode, func(t *testing.T) {
			var service *attachedmsg.Service
			switch mode {
			case "disabled":
				service = attachedmsg.New(attachedmsg.Options{Origin: origin})
			case "unqualified":
				service = attachedmsg.New(attachedmsg.Options{Enabled: true, Origin: origin})
			case "detached", "expired":
				service = qualifiedNotes()
			}
			f, key, in := watchFixture(t, service)
			v := activateWatch(t, f, key, &in)
			if mode == "detached" {
				if _, e := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET state='detached' WHERE id=$1`, in.RequestID); e != nil {
					t.Fatal(e)
				}
			} else if mode == "expired" {
				if _, e := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, in.RequestID); e != nil {
					t.Fatal(e)
				}
			}
			var recipient string
			if e := f.db.Admin.QueryRow(t.Context(), `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1`, *v.SessionID).Scan(&recipient); e != nil {
				t.Fatal(e)
			}
			for _, compat := range []bool{false, true} {
				for _, session := range []bool{false, true} {
					path := "/api/inbox/messages"
					b := map[string]any{"recipient_principal_id": recipient, "body": "ordinary tell or chat", "idempotency_key": attachedmsg.UUID()}
					if compat {
						path = "/api/projects/" + in.Snapshot.ProjectID + "/messages"
						delete(b, "recipient_principal_id")
						b["to"] = recipient
					}
					if session {
						b["recipient_session_id"] = *v.SessionID
					}
					var msg inbox.Message
					decodeResult(t, f.call("POST", path, b, true, "", 201), &msg)
					if msg.Body != b["body"] || msg.ContentMode != "" {
						t.Fatal("ordinary message entered attached path")
					}
					b["idempotency_key"] = attachedmsg.UUID()
					if !compat {
						b["reply_to_id"] = msg.ID
						f.call("POST", path, b, true, "", 201)
						continue
					}
					for _, field := range []string{"delivery_level", "thread_id", "is_action_request", "expects_reply", "reply_to"} {
						b["idempotency_key"] = attachedmsg.UUID()
						want := 201
						switch field {
						case "delivery_level":
							b[field] = "steer"
						case "thread_id":
							b[field] = "existing-behavior-thread"
						case "reply_to":
							b[field] = attachedmsg.UUID()
							want = 404
						default:
							b[field] = true
						}
						f.call("POST", path, b, true, "", want)
						delete(b, field)
					}
				}
			}
		})
	}
}

func TestAttachedNotificationFloodPreservesOwnerBudget(t *testing.T) {
	n := readyNotes(t)
	f := n.f
	var created struct {
		Token string `json:"token"`
	}
	decodeResult(t, f.call("POST", "/api/agent-keys", map[string]any{"name": "Flood agent", "scopes": []string{"inbox.send", "inbox.read", "nodes.read", "harness.read"}}, true, "", 201), &created)
	send := func(i, want int) {
		compat := i%2 == 0
		b := n.body(fmt.Sprint("flood-", i), "notification", compat)
		delete(b, "recipient_message_generation")
		f.call("POST", n.path(compat), b, false, created.Token, want)
	}
	for i := 0; i < 3; i++ {
		send(i, 201)
	}
	send(3, 429) // notification rate exhausted, owner's burst is untouched
	for i := 0; i < 3; i++ {
		f.call("POST", n.path(i%2 == 0), n.body(fmt.Sprint("owner-", i), "owner", i%2 == 0), true, "", 201)
	}
	if _, e := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET message_tokens=3,message_notice_tokens=3 WHERE id=$1`, n.in.RequestID); e != nil {
		t.Fatal(e)
	}
	send(4, 201)
	send(5, 201)
	send(6, 429) // notification memory exhausted
	for i := 3; i < 5; i++ {
		f.call("POST", n.path(i%2 == 0), n.body(fmt.Sprint("owner-", i), "owner", i%2 == 0), true, "", 201)
	}
	f.call("POST", n.path(false), n.body("owner-full", "owner", false), true, "", 429)
	var count int
	if e := f.db.Admin.QueryRow(t.Context(), `SELECT message_window_count FROM agent_pairing_computers WHERE id=$1`, n.in.ComputerID).Scan(&count); e != nil || count != 5 {
		t.Fatalf("owner counter = %d: %v", count, e)
	}
}

func TestAttachedTakeRequiresLiveRowInTransaction(t *testing.T) {
	n := readyNotes(t)
	var msg inbox.Message
	decodeResult(t, n.f.call("POST", n.path(false), n.body("row-guard", "row-guard-canary", false), true, "", 201), &msg)
	for _, state := range []string{"shown", "completed", "not_delivered", "cancelled", "expired", "uncertain", "deadline", "queued", "offered"} {
		t.Run(state, func(t *testing.T) {
			outcome, deadline := state, time.Now().Add(time.Minute)
			if state == "deadline" {
				outcome = "queued"
				deadline = time.Now().Add(-time.Second)
			}
			e := db.InTenant(dbtest.Seed(t.Context()), n.f.db.App, n.f.tenantID, func(tx pgx.Tx) error {
				if _, e := tx.Exec(t.Context(), `UPDATE inbox_messages SET attached_outcome=$2,message_deadline=$3 WHERE id=$1`, msg.ID, outcome, deadline); e != nil {
					return e
				}
				body, ok := n.s.Take(n.grant.Binding, n.grant.ID, msg.ID, attachedmsg.TakeTransaction{Context: t.Context(), Tx: tx})
				want := state == "queued" || state == "offered"
				if ok != want || (want && body != "row-guard-canary") || (!want && body != "") {
					t.Fatalf("Take row guard failed for %s", state)
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if state == "queued" { // re-reserve only for the second positive control
				if e := n.s.Reserve(msg.ID, n.grant.Binding, n.grant.ID, "row-guard-canary", "guard", time.Now().Add(time.Minute)); e != nil {
					t.Fatal(e)
				}
				n.s.Publish(n.f.tenantID, msg.ID)
			}
		})
	}
}

func TestAttachedEventPrivacyMalformedSender(t *testing.T) {
	n := readyNotes(t)
	var id int64
	e := db.InTenant(dbtest.Seed(t.Context()), n.f.db.App, n.f.tenantID, func(tx pgx.Tx) error {
		ev, e := events.Append(t.Context(), tx, tenant.Principal{ID: n.f.person, TenantID: n.f.tenantID, Kind: tenant.Person}, events.Change{Type: "inbox.attached_sent", After: map[string]string{"sender_principal_id": "s"}})
		id = ev.ID
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: n.recipient, TenantID: n.f.tenantID, Kind: tenant.Agent})
	e = db.InTenant(ctx, n.f.db.App, n.f.tenantID, func(tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE id=$1`, id).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			t.Fatal("malformed attached event visible to non-sender")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
