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
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/dns/dnsmessage"

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
	if adapter.PrincipalID != w.agent.ID || adapter.Vendor != "grok" || adapter.DeliveryMode != "push" || adapter.Version != 1 || !adapter.Enabled || adapter.LastSuccessAt != nil || adapter.LastFailureAt != nil || bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(endpoint)) {
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
	denied := mustJSON[map[string]any](t, post(readOnly, messagePath, string(reply), 403))
	if denied["reason_code"] != "missing_key_scope" || denied["scope"] != "inbox.send" {
		t.Fatalf("inbound negative failed for the wrong reason: %v", denied)
	}
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

// Risk: hostname validation can stall every tenant write while waiting for DNS.
// Pause resolution, then probe the actual tenant/tree fences and read transaction.
// Live sends must still reject authorization or target changes across that gap.
func testAssistantAdapterValidationWithoutWriteFences(t *testing.T) {
	resolver := assistantAdapterDNS(t)
	original := net.DefaultResolver
	net.DefaultResolver = resolver
	t.Cleanup(func() { net.DefaultResolver = original })
	for _, tc := range []struct {
		name   string
		dryRun bool
		change string
		status int
		state  string
	}{
		{"dry run", true, "", http.StatusOK, "validated"},
		{"live send", false, "", http.StatusAccepted, "queued"},
		{"live permission revoked", false, "revoke", http.StatusForbidden, "forbidden"},
		{"live target replaced", false, "replace", http.StatusConflict, "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, m, project, srv := messagingWorld(t)
			path := "/api/projects/" + project + "/assistant-adapters"
			registration := map[string]string{"vendor": "grok", "principal_id": w.agent.ID, "address": "grok_bot:worker", "endpoint": "https://hooks.example.org/assistant", "webhook_secret": "fixture-dns-webhook"}
			status, data := compatPost(t, srv, w.admin, path, registration)
			if status != http.StatusCreated {
				t.Fatalf("register hostname target: %d %s", status, data)
			}
			adapter := mustJSON[AssistantAdapterConfig](t, data)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			var backend int
			m.databaseClock = func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
				var now time.Time
				err := tx.QueryRow(ctx, `SELECT clock_timestamp(),pg_backend_pid()`).Scan(&now, &backend)
				return now, err
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var enterOnce, releaseOnce sync.Once
			releaseDNS := func() { releaseOnce.Do(func() { close(release) }) }
			net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				enterOnce.Do(func() { close(entered) })
				select {
				case <-release:
					return resolver.Dial(ctx, network, address)
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}
			t.Cleanup(func() { net.DefaultResolver = resolver })
			body, err := json.Marshal(map[string]any{"dry_run": tc.dryRun, "body": "hostname test send", "idempotency_key": "hostname-test"})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, path+"/"+adapter.ID+"/test-send", bytes.NewReader(body))
			req.SetPathValue("projectId", project)
			req.SetPathValue("adapterId", adapter.ID)
			req = req.WithContext(tenant.WithPrincipal(ctx, w.admin))
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				m.testAssistantAdapter(response, req)
			}()
			t.Cleanup(func() {
				releaseDNS()
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("test-send shutdown hung")
				}
			})
			select {
			case <-entered:
			case <-done:
				t.Fatalf("test-send skipped hostname resolution: %d %s", response.Code, response.Body)
			case <-ctx.Done():
				t.Fatal("test-send did not reach the DNS barrier")
			}
			var readCommitted bool
			if err := w.db.Admin.QueryRow(ctx, `SELECT xact_start IS NULL FROM pg_stat_activity WHERE pid=$1`, backend).Scan(&readCommitted); err != nil {
				t.Fatal(err)
			}
			if !readCommitted {
				t.Error("DNS lookup retained the adapter read transaction")
			}
			if err := db.InTenant(dbtest.Seed(ctx), w.db.App, w.admin.TenantID, func(tx pgx.Tx) error {
				var id string
				if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, w.admin.TenantID).Scan(&id); err != nil {
					return fmt.Errorf("DNS lookup retained the tenant write fence: %w", err)
				}
				var free bool
				if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, w.admin.TenantID).Scan(&free); err != nil {
					return err
				}
				if !free {
					return fmt.Errorf("DNS lookup retained the tree write fence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			switch tc.change {
			case "revoke":
				dbtest.BindRole(t, w.db, w.admin.TenantID, w.admin.ID, "viewer")
			case "replace":
				registration["endpoint"] = "https://8.8.8.8/replacement"
				status, data := compatPost(t, srv, w.admin, path, registration)
				if status != http.StatusCreated || mustJSON[AssistantAdapterConfig](t, data).ID == adapter.ID {
					t.Fatalf("replace target during DNS: %d %s", status, data)
				}
			}
			releaseDNS()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("test-send did not finish after DNS release")
			}
			if response.Code != tc.status {
				t.Fatalf("test-send: %d want %d: %s", response.Code, tc.status, response.Body)
			}
			wantMessages := 0
			if tc.status >= 400 {
				failure := mustJSON[map[string]any](t, response.Body.Bytes())
				if failure["code"] != tc.state {
					t.Fatalf("test-send failed for the wrong reason: %s", response.Body)
				}
			} else {
				result := mustJSON[assistantAdapterTestResult](t, response.Body.Bytes())
				if result.AdapterID != adapter.ID || result.DryRun != tc.dryRun || result.State != tc.state || (result.MessageID != "") == tc.dryRun {
					t.Fatalf("dishonest test-send result: %s", response.Body)
				}
				if !tc.dryRun {
					wantMessages = 1
				}
			}
			if got := countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_compat_messages`); got != wantMessages {
				t.Fatalf("queued messages: %d want %d", got, wantMessages)
			}
		})
	}
}

func assistantAdapterDNS(t *testing.T) *net.Resolver {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buf[:n]) != nil || len(query.Questions) != 1 {
				continue
			}
			answer := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true}, Questions: query.Questions}
			if question := query.Questions[0]; question.Type == dnsmessage.TypeA {
				answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{8, 8, 8, 8}}}}
			}
			if data, err := answer.Pack(); err == nil {
				_, _ = conn.WriteTo(data, addr)
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
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

// Risk: a frozen routine host gets failed/retried instead of retaining its
// inbox, or polling is mistaken for acknowledgement. Every age/retry decision
// below uses one database-derived clock, advanced without sleeping.
func testAssistantPullRoundTrip(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m}}
	if _, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(server.Handler())
	t.Cleanup(srv.Close)
	owner := receiptSession(t, w, w.admin)
	key := receiptAgentKey(t, w, w.agent, "inbox.read", "inbox.send", "inbox.receipt")
	sendOnly := receiptAgentKey(t, w, w.agent, "inbox.send")
	request := func(credential, method, path, body string, want int) []byte {
		t.Helper()
		status, data := receiptRequest(t, srv, credential, method, path, body)
		if status != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, status, want, data)
		}
		return data
	}
	path := "/api/projects/" + project + "/assistant-adapters"
	body := fmt.Sprintf(`{"vendor":"grok","principal_id":%q,"address":"grok_bot:pull"}`, w.agent.ID)
	adapter := mustJSON[AssistantAdapterConfig](t, request(owner, "POST", path, body, 201))
	if adapter.DeliveryMode != "pull" || adapter.Implicit || adapter.LastPullAt != nil || adapter.OldestUnreadAt != nil {
		t.Fatalf("new Grok default is not an empty pull inbox: %+v", adapter)
	}
	// Explicit pull never accepts dormant endpoint credentials.
	request(owner, "POST", path, strings.TrimSuffix(body, "}")+`,"delivery_mode":"pull","endpoint":"https://8.8.8.8/unused"}`, 400)
	// An older generic webhook registration must not create a second push path.
	endpoint := "https://8.8.8.8/old-generic-wake"
	if _, err := m.base.createTarget(t.Context(), w.admin, w.agent.ID, "webhook", &endpoint); err != nil {
		t.Fatal(err)
	}
	testPath := path + "/" + adapter.ID + "/test-send"
	request(owner, "POST", testPath, `{}`, 200)
	live := `{"dry_run":false,"body":"retained while host sleeps","idempotency_key":"pull-roundtrip"}`
	queued := mustJSON[assistantAdapterTestResult](t, request(owner, "POST", testPath, live, 202))
	if again := mustJSON[assistantAdapterTestResult](t, request(owner, "POST", testPath, live, 202)); again.MessageID != queued.MessageID {
		t.Fatal("pull test-send duplicated its inbox row")
	}
	var now time.Time
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.Truncate(time.Microsecond)
	m.databaseClock = func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }
	m.base.laneClock = m.databaseClock
	oldest := now.Add(-2 * time.Hour)
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET created_at=$2 WHERE id=$1`, queued.MessageID, oldest); err != nil {
		t.Fatal(err)
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_receipts WHERE message_id=$1 AND deliver_by IS NULL`, queued.MessageID) != 1 {
		t.Fatal("pull inbox inherited the online delivery deadline")
	}
	// Simulate a pre-upgrade queued receipt whose automatic deadline elapsed.
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=$2 WHERE message_id=$1`, queued.MessageID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if failed, err := NewSweeper(w.db.App).SweepTenant(t.Context(), w.admin.TenantID); err != nil || failed != 0 {
		t.Fatalf("suspended pull host failed by sweeper: failed=%d err=%v", failed, err)
	}
	assertNoPush := func() {
		t.Helper()
		d := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
			t.Fatal("pull adapter made an outbound request")
			return nil, nil
		})}}
		for range 3 {
			if worked, err := d.DispatchOne(t.Context(), w.admin.TenantID); err != nil || worked {
				t.Fatalf("pull adapter entered push retry loop: worked=%v err=%v", worked, err)
			}
		}
		if work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "grok_bot:pull", Adapter: "grok_bot_routine"}); err != nil || work != nil {
			t.Fatalf("pull adapter accepted a push claim: work=%+v err=%v", work, err)
		}
		if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_message_deliveries WHERE message_id=$1 AND attempts=0 AND lease_token IS NULL AND lease_until IS NULL`, queued.MessageID) != 1 || countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_wakes WHERE message_id=$1`, queued.MessageID) != 0 {
			t.Fatal("pull accumulated outbound attempts or wake jobs")
		}
	}
	assertNoPush()
	health := func() AssistantAdapterConfig {
		t.Helper()
		return mustJSON[AssistantAdapterConfig](t, request(owner, "GET", path+"/"+adapter.ID, "", 200))
	}
	before := health()
	if before.LastPullAt != nil || before.OldestUnreadAt == nil || !before.OldestUnreadAt.Equal(oldest) || before.OldestUnreadAge == nil || *before.OldestUnreadAge != 7200 || before.LastFailureAt != nil {
		t.Fatalf("pull health did not describe unread age: %+v", before)
	}
	request(sendOnly, "GET", "/api/inbox/messages?wait_ms=0", "", 403)
	if health().LastPullAt != nil {
		t.Fatal("denied poll refreshed liveness")
	}
	for range 2 {
		page := mustJSON[Page](t, request(key, "GET", "/api/inbox/messages?wait_ms=0", "", 200))
		if len(page.Items) != 1 || page.Items[0].ID != queued.MessageID {
			t.Fatalf("unacknowledged pull is not replayable: %+v", page)
		}
	}
	if got := mustJSON[Receipt](t, request(owner, "GET", queued.ReceiptPath, "", 200)); got.State != "queued" {
		t.Fatalf("fetch claimed receiver acknowledgement: %+v", got)
	}
	now = now.Add(3 * time.Hour)
	stale := health()
	if stale.LastPullAt == nil || !stale.LastPullAt.Equal(now.Add(-3*time.Hour)) || stale.OldestUnreadAge == nil || *stale.OldestUnreadAge != 18000 || stale.LastFailureAt != nil {
		t.Fatalf("stale polling reported a push failure: %+v", stale)
	}
	assertNoPush()
	request(key, "POST", "/api/inbox/messages/"+queued.MessageID+"/ack", `{}`, 200)
	request(key, "POST", "/api/inbox/messages/"+queued.MessageID+"/ack", `{}`, 200)
	if got := mustJSON[Receipt](t, request(owner, "GET", queued.ReceiptPath, "", 200)); got.State != "handed_off" || got.TargetID == nil || *got.TargetID != adapter.ID {
		t.Fatalf("pull acknowledgement lost receipt binding: %+v", got)
	}
	request(key, "GET", "/api/inbox/messages?wait_ms=0", "", 200)
	if got := health(); got.LastPullAt == nil || !got.LastPullAt.Equal(now) || got.OldestUnreadAt != nil || got.OldestUnreadAge != nil || got.LastSuccessAt != nil || got.LastFailureAt != nil {
		t.Fatalf("empty poll did not refresh mode-specific health: %+v", got)
	}
	assertNoPush()
	// The UI and ordinary CLI can address the principal inbox directly, without
	// selecting a project target. They must retain the same pull behaviour.
	raw := mustJSON[Message](t, request(owner, "POST", "/api/inbox/messages", sendJSON(w.agent.ID, "direct pull inbox", "direct-pull", nil, nil), 201))
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_wakes WHERE message_id=$1`, raw.ID) != 0 || countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_receipts WHERE message_id=$1 AND deliver_by IS NULL`, raw.ID) != 1 {
		t.Fatal("direct principal inbox bypassed pull mode")
	}
	// Explicit expiry remains authoritative even though automatic online
	// deadlines are suppressed. Derive it from the database clock as well.
	if _, err := w.db.Admin.Exec(t.Context(), `WITH instant AS MATERIALIZED (SELECT clock_timestamp() AS now)
	 UPDATE inbox_messages SET created_at=instant.now-interval '2 minutes',expires_at=instant.now-interval '1 minute'
	 FROM instant WHERE id=$1`, raw.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts r SET deliver_by=m.expires_at FROM inbox_messages m WHERE r.message_id=m.id AND m.id=$1`, raw.ID); err != nil {
		t.Fatal(err)
	}
	if failed, err := NewSweeper(w.db.App).SweepTenant(t.Context(), w.admin.TenantID); err != nil || failed != 1 {
		t.Fatalf("pull ignored explicit expiry: failed=%d err=%v", failed, err)
	}
	if got := mustJSON[Receipt](t, request(owner, "GET", "/api/inbox/messages/"+raw.ID+"/receipt", "", 200)); got.State != "failed" {
		t.Fatalf("expired pull receipt reported success: %+v", got)
	}
}

// Risk: installing pull support silently requires Amy's owner to re-register
// the assistant or replace its principal/key. Provision with the old lifecycle
// before the expansion, then authenticate with that exact key afterwards.
func testAssistantPullMigration(t *testing.T) {
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
	var m *messaging
	var srv *httptest.Server
	var w *world
	var owner, project string
	var amy struct {
		ID          string `json:"id"`
		PrincipalID string `json:"principal_id"`
		Token       string `json:"token"`
	}
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1436_assistant_pull_health.sql" {
			return nil
		}
		tid := insertTenant(t, d, "amy-pull-upgrade")
		person := insertPrincipal(t, d, tid, tenant.Person, "owner", []string{"admin"})
		w = &world{db: d, admin: person}
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'AMY-1','Amy inbox' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project)
		}); err != nil {
			return err
		}
		mod, err := NewMessaging(d.App, bytes.Repeat([]byte{7}, 32))
		if err != nil {
			return err
		}
		m = mod.(*messaging)
		server := &httpapi.Server{Pool: d.App, Modules: []httpapi.Module{m.base, m}}
		a, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)})
		if err != nil {
			return err
		}
		a.AssistantNoticeTx = MessagingAssistantNoticeTx
		srv = httptest.NewTLSServer(server.Handler())
		t.Cleanup(srv.Close)
		owner = receiptSession(t, w, person)
		status, data := receiptRequest(t, srv, owner, "POST", "/api/agents/assistants", fmt.Sprintf(`{"name":"Amy existing routine","expires_at":%q}`, time.Now().Add(7*24*time.Hour).UTC().Format(time.RFC3339Nano)))
		if status != 201 || json.Unmarshal(data, &amy) != nil || amy.PrincipalID == "" || amy.Token == "" {
			t.Fatalf("pre-upgrade Amy provisioning failed: status=%d", status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + project + "/assistant-adapters"
	status, data := receiptRequest(t, srv, owner, "GET", path+"/"+amy.PrincipalID, "")
	adapter := mustJSON[AssistantAdapterConfig](t, data)
	if status != 200 || !adapter.Implicit || adapter.DeliveryMode != "pull" || adapter.PrincipalID != amy.PrincipalID || adapter.ID != amy.PrincipalID || adapter.LastPullAt != nil || countSQL(t, d.App, w.admin.TenantID, `SELECT count(*) FROM inbox_message_targets WHERE principal_id=$1`, amy.PrincipalID) != 0 {
		t.Fatalf("existing assistant required owner migration: %d %+v", status, adapter)
	}
	// Its scoped key has not been rotated, granted new scopes or given roles.
	if countSQL(t, d.App, w.admin.TenantID, `SELECT count(*) FROM agent_keys WHERE id=$1 AND principal_id=$2 AND revoked_at IS NULL AND scopes=ARRAY['inbox.read','inbox.send','inbox.receipt']::text[]`, amy.ID, amy.PrincipalID) != 1 {
		t.Fatal("upgrade changed existing Amy key metadata")
	}
	status, data = receiptRequest(t, srv, owner, "POST", path+"/"+amy.PrincipalID+"/test-send", `{"dry_run":false,"body":"Amy pulls during a run","idempotency_key":"amy-existing-pull"}`)
	queued := mustJSON[assistantAdapterTestResult](t, data)
	if status != 202 {
		t.Fatalf("implicit pull test-send failed: %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, amy.Token, "GET", "/api/inbox/messages?wait_ms=0", "")
	page := mustJSON[Page](t, data)
	if status != 200 || len(page.Items) != 1 || page.Items[0].ID != queued.MessageID {
		t.Fatalf("original Amy key cannot pull after expansion: %d %s", status, data)
	}
	status, _ = receiptRequest(t, srv, amy.Token, "POST", "/api/inbox/messages/"+queued.MessageID+"/ack", `{}`)
	if status != 200 {
		t.Fatalf("original Amy key cannot acknowledge: %d", status)
	}
	status, data = receiptRequest(t, srv, owner, "GET", queued.ReceiptPath, "")
	if got := mustJSON[Receipt](t, data); status != 200 || got.State != "handed_off" {
		t.Fatalf("implicit assistant receipt failed: %d %+v", status, got)
	}
	status, data = receiptRequest(t, srv, owner, "GET", path+"/"+amy.PrincipalID, "")
	if got := mustJSON[AssistantAdapterConfig](t, data); status != 200 || got.LastPullAt == nil || got.OldestUnreadAt != nil {
		t.Fatalf("implicit pull health missing: %d %+v", status, got)
	}
	// The same lifecycle endpoint used by `aeon agents assistant rotate` stays
	// valid and retains the existing principal while revoking the old key.
	status, data = receiptRequest(t, srv, owner, "POST", "/api/agents/assistants/"+amy.PrincipalID+"/rotate", fmt.Sprintf(`{"expires_at":%q}`, time.Now().Add(7*24*time.Hour).UTC().Format(time.RFC3339Nano)))
	var rotated struct {
		PrincipalID string `json:"principal_id"`
		Token       string `json:"token"`
	}
	if status != 201 || json.Unmarshal(data, &rotated) != nil || rotated.PrincipalID != amy.PrincipalID || rotated.Token == "" {
		t.Fatalf("existing assistant rotate changed identity: status=%d", status)
	}
	if status, _ := receiptRequest(t, srv, amy.Token, "GET", "/api/inbox/messages?wait_ms=0", ""); status != 401 {
		t.Fatalf("old Amy key remains active after rotate: %d", status)
	}
	if status, _ := receiptRequest(t, srv, rotated.Token, "GET", "/api/inbox/messages?wait_ms=0", ""); status != 200 {
		t.Fatalf("rotated Amy key cannot poll: %d", status)
	}
}

// Risk: a sleeping pull assistant loses unread work after the online deadline,
// retention silently lasts forever, or a revoked assistant still looks healthy.
// One DB-derived clock drives sweep boundaries; no calendar pins or sleeps.
func TestPullAssistantPickupRetention(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		name := "configured pull adapter"
		if implicit {
			name = "implicit messaging assistant"
		}
		t.Run(name, func(t *testing.T) {
			w, m, project, _ := messagingWorld(t)
			server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m}}
			a, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)})
			if err != nil {
				t.Fatal(err)
			}
			a.AssistantNoticeTx = MessagingAssistantNoticeTx
			srv := httptest.NewTLSServer(server.Handler())
			t.Cleanup(srv.Close)
			owner := receiptSession(t, w, w.admin)
			request := func(credential, method, path, body string, want int) []byte {
				t.Helper()
				status, data := receiptRequest(t, srv, credential, method, path, body)
				if status != want {
					t.Fatalf("%s %s: status %d want %d", method, path, status, want)
				}
				return data
			}
			var now time.Time
			if err := w.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
				t.Fatal(err)
			}
			now = now.Truncate(time.Microsecond)
			recipient := w.agent.ID
			var credential, adapterID string
			if implicit {
				var assistant struct {
					PrincipalID string `json:"principal_id"`
					Token       string `json:"token"`
				}
				data := request(owner, "POST", "/api/agents/assistants", fmt.Sprintf(`{"name":"Amy pickup retention","expires_at":%q}`, now.Add(29*24*time.Hour).Format(time.RFC3339Nano)), 201)
				if err := json.Unmarshal(data, &assistant); err != nil {
					t.Fatal(err)
				}
				recipient, credential = assistant.PrincipalID, assistant.Token
			} else {
				credential = receiptAgentKey(t, w, w.agent, "inbox.read", "inbox.send", "inbox.receipt")
				body := fmt.Sprintf(`{"vendor":"grok","principal_id":%q,"address":"grok_bot:retention","delivery_mode":"pull"}`, recipient)
				adapterID = mustJSON[AssistantAdapterConfig](t, request(owner, "POST", "/api/projects/"+project+"/assistant-adapters", body, 201)).ID
			}
			sweeper := NewSweeper(w.db.App)
			sweeper.clock = func() time.Time { return now }
			sweep := func(want int) {
				t.Helper()
				n, err := sweeper.SweepTenant(t.Context(), w.admin.TenantID)
				if err != nil || n != want {
					t.Fatalf("sweep failed=%d want=%d err=%v", n, want, err)
				}
			}
			send := func(key string) Message {
				t.Helper()
				var msg Message
				if !implicit && (key == "retention-bound" || key == "disabled-assistant") {
					queued := mustJSON[assistantAdapterTestResult](t, request(owner, "POST", "/api/projects/"+project+"/assistant-adapters/"+adapterID+"/test-send", fmt.Sprintf(`{"dry_run":false,"body":"waiting while host sleeps","idempotency_key":%q}`, key), 202))
					msg.ID = queued.MessageID
					if err := w.db.Admin.QueryRow(t.Context(), `SELECT created_at FROM inbox_messages WHERE id=$1`, msg.ID).Scan(&msg.CreatedAt); err != nil {
						t.Fatal(err)
					}
				} else {
					msg = mustJSON[Message](t, request(owner, "POST", "/api/inbox/messages", sendJSON(recipient, "waiting while host sleeps", key, nil, nil), 201))
				}
				// Acceptance is stamped by Postgres. Rebase the injected clock on that
				// exact stamp; every duration below then has a single origin.
				now = msg.CreatedAt
				return msg
			}
			status := func(id string) MessageStatus {
				t.Helper()
				page := mustJSON[struct {
					Items []MessageStatus `json:"items"`
				}](t, request(owner, "GET", "/api/inbox/message-status?ids="+id, "", 200))
				if len(page.Items) != 1 {
					t.Fatalf("missing sender status for %s", id)
				}
				return page.Items[0]
			}
			receipt := func(id string) Receipt {
				t.Helper()
				return mustJSON[Receipt](t, request(owner, "GET", "/api/inbox/messages/"+id+"/receipt", "", 200))
			}
			msg := send("pickup-after-two-hours")
			if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_receipts WHERE message_id=$1 AND deliver_by IS NULL`, msg.ID) != 1 {
				t.Fatal("pull has an online deadline")
			}
			if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events WHERE type='inbox.receipt_queued' AND after->>'message_id'=$1 AND (after ? 'target_id' OR after ? 'target_version' OR after ? 'adapter' OR after ? 'address')`, msg.ID) != 0 {
				t.Fatal("direct receipt event exposed an implicit project binding")
			}
			// Old writers may already have attached a 30-minute deadline. It cannot
			// override the recipient's pull mode after upgrade.
			msg.CreatedAt = msg.CreatedAt.Add(-2 * time.Hour)
			now = msg.CreatedAt
			if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET created_at=$2 WHERE id=$1`, msg.ID, now); err != nil {
				t.Fatal(err)
			}
			if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=$2 WHERE message_id=$1`, msg.ID, now.Add(30*time.Minute)); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Hour)
			if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_receipts WHERE message_id=$1 AND deliver_by<=clock_timestamp()`, msg.ID) != 1 {
				t.Fatal("fixture never crossed the legacy online deadline")
			}
			if !implicit {
				// A replacement may change the online mode. This message keeps
				// its original pull binding; new push messages keep 30 minutes.
				body := fmt.Sprintf(`{"vendor":"grok","principal_id":%q,"address":"grok_bot:retention","delivery_mode":"push","endpoint":"https://8.8.8.8/pickup-fixture","webhook_secret":"fixture-pickup-webhook"}`, recipient)
				request(owner, "POST", "/api/projects/"+project+"/assistant-adapters", body, 201)
			}
			sweep(0)
			if got := status(msg.ID); got.Status != "sent" || !got.WaitingForPickup || got.Reason != "" {
				t.Fatalf("sender does not see waiting for pickup: %+v", got)
			}
			if got := receipt(msg.ID); got.State != "queued" || got.FailureReason != "" || got.TargetID != nil || got.TargetVersion != nil || got.Adapter != "" || got.Address != "" {
				t.Fatalf("sleeping host failed: %+v", got)
			}
			page := mustJSON[Page](t, request(credential, "GET", "/api/inbox/messages?wait_ms=0", "", 200))
			if len(page.Items) != 1 || page.Items[0].ID != msg.ID {
				t.Fatalf("next pull lost message: %+v", page)
			}
			if got := status(msg.ID); got.Status != "delivered" || got.WaitingForPickup {
				t.Fatalf("pickup status: %+v", got)
			}
			request(credential, "POST", "/api/inbox/messages/"+msg.ID+"/ack", "{}", 200)
			if got := receipt(msg.ID); got.State != "handed_off" || got.HandedOffAt == nil {
				t.Fatalf("missing acknowledgement receipt: %+v", got)
			}
			if !implicit {
				push := send("push-deadline-unchanged")
				var seconds float64
				guaranteeRow(t, w, `SELECT extract(epoch FROM r.deliver_by-m.created_at) FROM inbox_receipts r JOIN inbox_messages m ON m.id=r.message_id WHERE m.id=$1`, []any{push.ID}, &seconds)
				if seconds != 1800 || status(push.ID).WaitingForPickup {
					t.Fatal("push inherited pull delivery semantics")
				}
				expireDeadline(t, w, push.ID)
				sweep(1)
				if got := receipt(push.ID); got.State != "failed" || got.FailureReason != ReasonNoListener {
					t.Fatalf("push deadline changed: %+v", got)
				}
				body := fmt.Sprintf(`{"vendor":"grok","principal_id":%q,"address":"grok_bot:retention","delivery_mode":"pull"}`, recipient)
				adapterID = mustJSON[AssistantAdapterConfig](t, request(owner, "POST", "/api/projects/"+project+"/assistant-adapters", body, 201)).ID
			}
			retained := send("retention-bound")
			now = now.Add(time.Duration(pullRetentionDays)*24*time.Hour - time.Microsecond)
			sweep(0)
			if got := receipt(retained.ID); got.State != "queued" {
				t.Fatalf("retention failed early: %+v", got)
			}
			now = now.Add(time.Microsecond)
			sweep(1)
			if got := receipt(retained.ID); got.State != "failed" || got.FailureReason != ReasonPickupExpired || got.HandedOffAt != nil {
				t.Fatalf("wrong retention outcome: %+v", got)
			}
			if got := status(retained.ID); got.Status != "not_delivered" || got.Reason != ReasonPickupExpired || got.WaitingForPickup {
				t.Fatalf("wrong expired sender status: %+v", got)
			}
			notices := noticesFor(t, w, retained.ID)
			if len(notices) != 1 || !strings.Contains(notices[""].body, "not picked up within 7 days") {
				t.Fatalf("retention notice: %+v", notices)
			}
			sweep(0)
			if len(noticesFor(t, w, retained.ID)) != 1 {
				t.Fatal("duplicate retention notice")
			}
			request(credential, "POST", "/api/inbox/messages/"+retained.ID+"/ack", "{}", 409)
			page = mustJSON[Page](t, request(credential, "GET", "/api/inbox/messages?wait_ms=0", "", 200))
			if len(page.Items) != 0 {
				t.Fatalf("expired message resurrected: %+v", page)
			}
			rotating := send("rotation-preserves-pickup")
			if implicit {
				var replacement struct {
					Token string `json:"token"`
				}
				data := request(owner, "POST", "/api/agents/assistants/"+recipient+"/rotate", fmt.Sprintf(`{"expires_at":%q}`, now.Add(29*24*time.Hour).Format(time.RFC3339Nano)), 201)
				if err := json.Unmarshal(data, &replacement); err != nil {
					t.Fatal(err)
				}
				credential = replacement.Token
			} else {
				credential = receiptAgentKey(t, w, w.agent, "inbox.read", "inbox.send")
				if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=$2 WHERE principal_id=$1 AND 'inbox.receipt'=ANY(scopes)`, recipient, now); err != nil {
					t.Fatal(err)
				}
			}
			sweep(0)
			if got := receipt(rotating.ID); got.State != "queued" {
				t.Fatalf("rotation failed pending message: %+v", got)
			}
			request(credential, "POST", "/api/inbox/messages/"+rotating.ID+"/ack", "{}", 200)
			revoked := send("revoked-inbox-key")
			if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=$2 WHERE principal_id=$1`, recipient, now); err != nil {
				t.Fatal(err)
			}
			sweep(1)
			if got := receipt(revoked.ID); got.State != "failed" || got.FailureReason != ReasonAssistantRevoked {
				t.Fatalf("revocation waited for a deadline: %+v", got)
			}
			if len(noticesFor(t, w, revoked.ID)) != 1 {
				t.Fatal("missing revocation notice")
			}
			disabled := send("disabled-assistant")
			if implicit {
				request(owner, "DELETE", "/api/agents/assistants/"+recipient, "", 204)
			} else {
				if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_targets SET enabled=false WHERE id=$1`, adapterID); err != nil {
					t.Fatal(err)
				}
			}
			sweep(1)
			if got := receipt(disabled.ID); got.State != "failed" || got.FailureReason != ReasonAssistantDisabled {
				t.Fatalf("disable waited for a deadline: %+v", got)
			}
			if !implicit && countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_message_deliveries WHERE message_id=$1 AND state='dead' AND reason='unavailable'`, disabled.ID) != 1 {
				t.Fatal("disabled compat delivery did not become terminal")
			}
			if len(noticesFor(t, w, disabled.ID)) != 1 {
				t.Fatal("missing disabled notice")
			}
		})
	}
}
