// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a new registry could bypass the inbox's fencing, deliver twice on
// retry, leak credentials, or claim delivery before the receiver accepts it.
// This extends the existing registered routine behaviour test, leaving the
// coordinator-owned tier records untouched. Every retry boundary uses one
// clock derived from the database; no timer or calendar instant drives it.
func testAssistantAdapterRoundTrip(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m}}
	if _, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(server.Handler())
	t.Cleanup(srv.Close)
	owner := receiptSession(t, w, w.admin)
	outsider := receiptSession(t, w, w.outsider)
	member := receiptSession(t, w, w.sender)
	key := receiptAgentKey(t, w, w.agent, "inbox.read", "inbox.send", "inbox.receipt")
	readOnly := receiptAgentKey(t, w, w.agent, "inbox.read")
	manager := insertPrincipal(t, w.db, w.admin.TenantID, tenant.Agent, "adapter-manager", nil)
	dbtest.BindRole(t, w.db, manager.TenantID, manager.ID, "admin")
	managerKey := receiptAgentKey(t, w, manager, "inbox.manage", "inbox.send")
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$2 WHERE principal_id=$1`, manager.ID, w.admin.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + project + "/assistant-adapters"
	const secret = "fixture-assistant-webhook-credential"
	const endpoint = "https://8.8.8.8/fixture-assistant-hook"
	create, _ := json.Marshal(map[string]string{"vendor": "grok", "principal_id": w.agent.ID, "address": "grok_bot:custom-address", "endpoint": endpoint, "webhook_secret": secret})
	post := func(credential, path, body string, want int) []byte {
		t.Helper()
		status, data := receiptRequest(t, srv, credential, "POST", path, body)
		if status != want {
			t.Fatalf("POST %s: %d want %d: %s", path, status, want, data)
		}
		return data
	}
	post(member, path, string(create), 403)
	post(outsider, path, string(create), 403)
	// Prove the key reaches the same route's read permission before checking
	// the person-only write, so a missing role/scope cannot satisfy the test.
	if status, data := receiptRequest(t, srv, managerKey, "GET", path, ""); status != 200 {
		t.Fatalf("manager scope fixture cannot read registry: %d %s", status, data)
	}
	post(managerKey, path, string(create), 403)
	data := post(owner, path, string(create), 201)
	adapter := mustJSON[AssistantAdapterConfig](t, data)
	if adapter.PrincipalID != w.agent.ID || adapter.Vendor != "grok" || adapter.Version != 1 || !adapter.Enabled || adapter.LastSuccessAt != nil || adapter.LastFailureAt != nil || bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(endpoint)) {
		t.Fatal("invalid or unredacted adapter registration")
	}
	testPath := path + "/" + adapter.ID + "/test-send"
	post(managerKey, testPath, `{}`, 403)
	// The dry-run defaults to no outbound request and creates no inbox row.
	preview := mustJSON[assistantAdapterTestResult](t, post(owner, testPath, `{}`, 200))
	if !preview.DryRun || preview.State != "validated" || preview.MessageID != "" || countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_message_deliveries`) != 0 {
		t.Fatal("dry-run queued work or claimed delivery")
	}
	if status, _ := receiptRequest(t, srv, key, "GET", path, ""); status != 403 {
		t.Fatalf("narrow assistant key read management data: %d", status)
	}
	if status, _ := receiptRequest(t, srv, outsider, "GET", path+"/"+adapter.ID, ""); status != 403 {
		t.Fatalf("foreign tenant read adapter: %d", status)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.outsider.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_adapters`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("adapter tenant RLS leak")
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO assistant_adapters(tenant_id,target_id,vendor) VALUES($1,$2,'grok')`, w.admin.TenantID, adapter.ID)
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Fatalf("cross-tenant write did not fail for RLS: %v", err)
		}
		// The deliberate rejection aborts this transaction.
		return err
	}); err == nil {
		t.Fatal("foreign tenant write succeeded")
	}
	live := `{"dry_run":false,"body":"assistant round trip","idempotency_key":"adapter-roundtrip"}`
	queued := mustJSON[assistantAdapterTestResult](t, post(owner, testPath, live, 202))
	again := mustJSON[assistantAdapterTestResult](t, post(owner, testPath, live, 202))
	if queued.MessageID == "" || queued.MessageID != again.MessageID || queued.State != "queued" || queued.DryRun {
		t.Fatal("test-send idempotency lost its durable inbox identity")
	}
	post(owner, testPath, strings.Replace(live, "assistant round trip", "different", 1), 409)
	receipt := func(want string) Receipt {
		t.Helper()
		status, data := receiptRequest(t, srv, owner, "GET", queued.ReceiptPath, "")
		got := mustJSON[Receipt](t, data)
		if status != 200 || got.State != want || got.TargetID == nil || *got.TargetID != adapter.ID {
			t.Fatalf("receipt: %d %s", status, data)
		}
		return got
	}
	receipt("queued")
	var now time.Time
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	m.databaseClock = func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }
	var payload []byte
	var deliveryID string
	calls := 0
	d := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		timestamp, signature := req.Header.Get("X-Aeon-Webhook-Timestamp"), req.Header.Get("X-Aeon-Webhook-Signature")
		mac := hmac.New(sha256.New, []byte(secret))
		fmt.Fprintf(mac, "%s.%s.", timestamp, req.Header.Get("Idempotency-Key"))
		mac.Write(body)
		if timestamp == "" || signature != "v1="+hex.EncodeToString(mac.Sum(nil)) || req.Header.Get("Authorization") != "Bearer "+secret || req.URL.String() != endpoint {
			t.Fatal("invalid signature or legacy receiver authentication")
		}
		var wake routineWake
		if err := json.Unmarshal(body, &wake); err != nil {
			t.Fatal(err)
		}
		if wake.MessageID != queued.MessageID || wake.DeliveryID != req.Header.Get("Idempotency-Key") || wake.Project == "" || !strings.Contains(wake.Content, "SECURITY NOTICE") || !strings.Contains(wake.Content, "assistant round trip") || bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(key)) || bytes.Contains(body, []byte(endpoint)) {
			t.Fatal("webhook lost its binding, untrusted framing or credential redaction")
		}
		if calls == 1 {
			payload, deliveryID = body, wake.DeliveryID
		} else if !bytes.Equal(payload, body) || deliveryID != wake.DeliveryID {
			t.Fatal("retry changed the payload or idempotency key")
		}
		status := 503
		if calls == 3 {
			status = 200
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("remote-private-error"))}, nil
	})}}
	for attempt, delay := range []time.Duration{2 * time.Second, 4 * time.Second} {
		worked, err := d.DispatchOne(t.Context(), w.admin.TenantID)
		if !worked || err == nil || calls != attempt+1 {
			t.Fatalf("failed attempt %d: worked=%v calls=%d err=%v", attempt+1, worked, calls, err)
		}
		var retryAt time.Time
		var attempts int
		if err := w.db.Admin.QueryRow(t.Context(), `SELECT attempts,lease_until FROM inbox_message_deliveries WHERE id=$1`, deliveryID).Scan(&attempts, &retryAt); err != nil {
			t.Fatal(err)
		}
		if attempts != attempt+1 || !retryAt.Equal(now.Add(delay)) {
			t.Fatalf("retry backoff: attempt=%d retry=%s now=%s delay=%s", attempts, retryAt, now, delay)
		}
		receipt("queued")
		now = retryAt.Add(-time.Microsecond)
		if worked, err := d.DispatchOne(t.Context(), w.admin.TenantID); worked || err != nil || calls != attempt+1 {
			t.Fatalf("live retry deadline reclaimed: worked=%v calls=%d err=%v", worked, calls, err)
		}
		now = retryAt
	}
	if worked, err := d.DispatchOne(t.Context(), w.admin.TenantID); !worked || err != nil || calls != 3 {
		t.Fatalf("successful retry: worked=%v calls=%d err=%v", worked, calls, err)
	}
	handedOff := receipt("handed_off")
	if handedOff.HandedOffAt == nil {
		t.Fatal("receiver confirmation lacks handed-off receipt timestamp")
	}
	if worked, err := d.DispatchOne(t.Context(), w.admin.TenantID); worked || err != nil || calls != 3 {
		t.Fatalf("confirmed message delivered again: worked=%v calls=%d err=%v", worked, calls, err)
	}
	status, data := receiptRequest(t, srv, owner, "GET", path+"/"+adapter.ID, "")
	health := mustJSON[AssistantAdapterConfig](t, data)
	if status != 200 || health.LastSuccessAt == nil || !health.LastSuccessAt.Equal(now) || health.LastFailureAt == nil || health.LastFailureCode == nil || *health.LastFailureCode != "http_error" || bytes.Contains(data, []byte("remote-private-error")) {
		t.Fatalf("dishonest or unredacted health: %d %s", status, data)
	}
	// Inbound is an ordinary API call authenticated with the bot's own key,
	// preserving sender attribution and the original thread/reply relationship.
	reply, _ := json.Marshal(compatSend{To: w.admin.ID, Body: "assistant scoped reply", Key: "adapter-inbound", ReplyTo: &queued.MessageID, Level: "simple"})
	messagePath := "/api/projects/" + project + "/messages"
	post(readOnly, messagePath, string(reply), 403)
	inbound := mustJSON[CompatMessage](t, post(key, messagePath, string(reply), 201))
	if inbound.SenderPrincipalID != w.agent.ID || inbound.RecipientPrincipalID != w.admin.ID || inbound.ReplyTo == nil || *inbound.ReplyTo != queued.MessageID {
		t.Fatal("inbound reply bypassed principal/key attribution")
	}
	if replay := mustJSON[CompatMessage](t, post(key, messagePath, string(reply), 201)); replay.ID != inbound.ID {
		t.Fatal("inbound retry duplicated the reply")
	}
	// Replacement keeps history but cannot silently test the new endpoint
	// through an old ID, and cannot move an address to another principal.
	replacement := mustJSON[AssistantAdapterConfig](t, post(owner, path, string(create), 201))
	if replacement.ID == adapter.ID || replacement.Version != 2 {
		t.Fatal("replacement overwrote the original target identity")
	}
	post(owner, testPath, live, 409)
	post(owner, path, strings.Replace(string(create), w.agent.ID, manager.ID, 1), 400)
	status, data = receiptRequest(t, srv, owner, "GET", path+"?limit=1", "")
	var page struct {
		Items     []AssistantAdapterConfig `json:"items"`
		NextAfter *string                  `json:"next_after"`
	}
	if json.Unmarshal(data, &page) != nil || status != 200 || len(page.Items) != 1 || page.NextAfter == nil {
		t.Fatalf("first bounded registry page: %d %s", status, data)
	}
	firstID := page.Items[0].ID
	status, data = receiptRequest(t, srv, owner, "GET", path+"?limit=1&after="+*page.NextAfter, "")
	if json.Unmarshal(data, &page) != nil || status != 200 || len(page.Items) != 1 || page.Items[0].ID == firstID || page.NextAfter != nil {
		t.Fatalf("next bounded registry page: %d %s", status, data)
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events WHERE coalesce(after::text,'') LIKE '%'||$1||'%' OR coalesce(after::text,'') LIKE '%'||$2||'%'`, secret, endpoint) != 0 {
		t.Fatal("endpoint credential leaked to audit")
	}
}

// Risk: authorization at request entry can be revoked while reading its body.
// The admission body callback is a barrier after the precheck and before the
// final transaction, and the retained stale Principal must fail RequireTx.
func testAssistantAdapterRevocation(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	body, _ := json.Marshal(map[string]string{"vendor": "grok", "principal_id": w.agent.ID, "address": "grok_bot:worker", "endpoint": "https://8.8.8.8/fixture-hook", "webhook_secret": "fixture-revocation-webhook"})
	req := httptest.NewRequest("POST", "/api/projects/"+project+"/assistant-adapters", nil)
	req.SetPathValue("projectId", project)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), w.admin))
	revoked := false
	req.Body = &messagingAdmissionBody{ReadCloser: io.NopCloser(bytes.NewReader(body)), pause: func() {
		revoked = true
		dbtest.BindRole(t, w.db, w.admin.TenantID, w.admin.ID, "viewer")
	}}
	written := httptest.NewRecorder()
	m.createAssistantAdapter(written, req)
	if !revoked || written.Code != 403 || !strings.Contains(written.Body.String(), "forbidden") {
		t.Fatalf("revoked registration: body barrier=%v code=%d body=%s", revoked, written.Code, written.Body)
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM assistant_adapters`) != 0 {
		t.Fatal("revoked caller wrote an adapter")
	}
}

// Risk: an expansion that works on an empty DB can lose old ciphertext or
// reject targets written by the previous binary. Seed before the exact new
// migration and run it with the restricted production-equivalent app owner.
func testAssistantAdapterMigration(t *testing.T) {
	ctx := dbtest.Seed(t.Context())
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var actor, bot tenant.Principal
	var project string
	var legacy MessageTarget
	var ciphertext []byte
	var m *messaging
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1435_assistant_adapters.sql" {
			return nil
		}
		tid := insertTenant(t, d, "adapter-upgrade")
		actor = insertPrincipal(t, d, tid, tenant.Person, "owner", []string{"admin"})
		bot = insertPrincipal(t, d, tid, tenant.Agent, "legacy", nil)
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'OLD-1','Existing project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project)
		}); err != nil {
			return err
		}
		mod, err := NewMessaging(d.App, bytes.Repeat([]byte{7}, 32))
		if err != nil {
			return err
		}
		m = mod.(*messaging)
		legacy, err = m.storeTarget(ctx, actor, project, targetInput{Address: "grok_bot:legacy", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/legacy-hook", Secret: "fixture-legacy-credential", Role: "primary", MaximumLevel: "simple"})
		if err != nil {
			return err
		}
		return d.Admin.QueryRow(ctx, `SELECT sealed_target FROM inbox_message_targets WHERE id=$1`, legacy.ID).Scan(&ciphertext)
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ID == "" {
		t.Fatal("upgrade hook did not seed a legacy target")
	}
	var stored []byte
	var vendor string
	if err := db.InTenant(ctx, d.App, bot.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT t.sealed_target,a.vendor FROM inbox_message_targets t JOIN assistant_adapters a ON a.tenant_id=t.tenant_id AND a.target_id=t.id WHERE t.id=$1`, legacy.ID).Scan(&stored, &vendor)
	}); err != nil || vendor != "grok" || !bytes.Equal(stored, ciphertext) {
		t.Fatalf("legacy target migration changed ciphertext or lost binding: vendor=%s err=%v", vendor, err)
	}
	// New code can deliver the migrated target without copying credentials.
	mustCompatSend(t, m, actor, project, compatInput("grok_bot:legacy", "legacy-after-upgrade"))
	calls := 0
	dispatcher := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer fixture-legacy-credential" || req.Header.Get("X-Aeon-Webhook-Signature") == "" {
			t.Fatal("migrated legacy receiver contract changed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	if worked, err := dispatcher.DispatchOne(ctx, bot.TenantID); !worked || err != nil || calls != 1 {
		t.Fatalf("migrated dispatch: worked=%v calls=%d err=%v", worked, calls, err)
	}
	// The same old target writer after migration goes through the DB bridge.
	next, err := m.storeTarget(ctx, actor, project, targetInput{Address: "grok_bot:legacy", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/new-legacy-hook", Secret: "fixture-legacy-credential", Role: "primary", MaximumLevel: "simple"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, bot.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT vendor FROM assistant_adapters WHERE target_id=$1`, next.ID).Scan(&vendor)
	}); err != nil || vendor != "grok" {
		t.Fatalf("old writer bridge: %s %v", vendor, err)
	}
}
