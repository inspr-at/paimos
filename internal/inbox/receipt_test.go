// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestInboxReceipt(t *testing.T) {
	t.Parallel()
	perm, ok := authz.Lookup("inbox.receipt")
	if !ok || !perm.AgentGrantable {
		t.Fatal("inbox.receipt must be an agent-grantable permission")
	}
	w, m, project, srv := messagingWorld(t)
	const secret = "RECEIPT-BODY-SECRET-do-not-leak"
	const key = "receipt-idem"

	status, body := do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secret, key, nil, nil), nil)
	if status != 201 {
		t.Fatalf("send %d %s", status, body)
	}
	first := mustJSON[Message](t, body)
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secret, key, nil, nil), nil)
	replay := mustJSON[Message](t, body)
	if status != 201 || replay.ID != first.ID {
		t.Fatalf("idempotent replay %d %s", status, body)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM inbox_receipts WHERE message_id=$1::uuid`, first.ID) != 1 {
		t.Fatal("replay wrote another receipt")
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type='inbox.receipt_queued' AND after->>'message_id'=$1`, first.ID) != 1 {
		t.Fatal("replay wrote another receipt event")
	}

	rec := mustReceipt(t, srv, w.sender.ID, first.ID)
	if rec.MessageID != first.ID || rec.IdempotencyKey != key || rec.Tenant != w.db.Name+"-a" ||
		rec.SenderPrincipalID != w.sender.ID || rec.RecipientPrincipalID != w.recipient.ID ||
		rec.State != "queued" || rec.HandedOffAt != nil || rec.FailureReason != "" ||
		rec.TargetID != nil || rec.TargetVersion != nil || rec.Adapter != "" || rec.Address != "" || rec.EffectiveLevel != "" {
		t.Fatalf("accepted receipt %+v", rec)
	}
	if strings.Contains(string(mustReceiptRaw(t, srv, w.sender.ID, first.ID)), secret) {
		t.Fatal("receipt leaked the body")
	}
	_, hidden := do(t, srv, w.sender.ID, http.MethodGet, "/api/inbox/messages/00000000-0000-4000-8000-000000000000/receipt", "", nil)
	for _, principal := range []tenant.Principal{w.recipient, w.admin, w.agent, w.outsider} {
		status, body = do(t, srv, principal.ID, http.MethodGet, "/api/inbox/messages/"+first.ID+"/receipt", "", nil)
		if status != 404 || string(body) != string(hidden) || strings.Contains(string(body), first.ID) || strings.Contains(string(body), secret) {
			t.Fatalf("non-sender %s %d %s", principal.Name, status, body)
		}
	}
	status, _ = do(t, srv, w.sender.ID, http.MethodGet, "/api/inbox/messages/not-a-uuid/receipt", "", nil)
	if status != 404 {
		t.Fatalf("bad id %d", status)
	}

	const senderKey = "fixture-routine-sender-key"
	const targetRef = "https://8.8.8.8/fixture-hook"
	target, err := m.storeTarget(t.Context(), w.admin, project, targetInput{
		Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook",
		Ref: targetRef, Secret: senderKey, Role: "primary", MaximumLevel: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	respond := func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	dispatcher := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(req *http.Request) (*http.Response, error) {
		return respond(req)
	})}}

	handed := sendCompat(t, m, w.sender, project, "grok_bot:worker", "hand-off", secret)
	queued := mustReceipt(t, srv, w.sender.ID, handed.ID)
	if queued.State != "queued" || queued.HandedOffAt != nil || queued.EffectiveLevel != "" ||
		queued.TargetID == nil || *queued.TargetID != target.ID || queued.TargetVersion == nil || *queued.TargetVersion != target.Version ||
		queued.Adapter != "grok_bot_routine" || queued.Address != "grok_bot:worker" ||
		queued.IdempotencyKey != "compat/"+handed.ID || queued.SenderPrincipalID != w.sender.ID {
		t.Fatalf("queued before confirmation %+v", queued)
	}
	if strings.Contains(string(mustReceiptRaw(t, srv, w.sender.ID, handed.ID)), secret) ||
		strings.Contains(string(mustReceiptRaw(t, srv, w.sender.ID, handed.ID)), senderKey) ||
		strings.Contains(string(mustReceiptRaw(t, srv, w.sender.ID, handed.ID)), targetRef) {
		t.Fatal("queued receipt leaked private material")
	}

	worked, err := dispatcher.DispatchOne(t.Context(), w.sender.TenantID)
	if err != nil || !worked {
		t.Fatalf("confirm worked=%t err=%v", worked, err)
	}
	confirmed := mustReceipt(t, srv, w.sender.ID, handed.ID)
	if confirmed.State != "handed_off" || confirmed.HandedOffAt == nil || confirmed.EffectiveLevel != "simple" ||
		confirmed.FailureReason != "" || confirmed.TargetID == nil || *confirmed.TargetID != target.ID {
		t.Fatalf("handed off %+v", confirmed)
	}
	if _, parseErr := time.Parse(time.RFC3339, *confirmed.HandedOffAt); parseErr != nil {
		t.Fatal(parseErr)
	}
	again := mustReceipt(t, srv, w.sender.ID, handed.ID)
	if again.State != "handed_off" || *again.HandedOffAt != *confirmed.HandedOffAt {
		t.Fatalf("handed_off_at moved %+v", again)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		return advanceReceipt(t.Context(), tx, w.agent, handed.ID, "failed", "", "late", receiptTarget{})
	}); err != nil {
		t.Fatal(err)
	}
	if stayed := mustReceipt(t, srv, w.sender.ID, handed.ID); stayed.State != "handed_off" || *stayed.HandedOffAt != *confirmed.HandedOffAt {
		t.Fatalf("handed_off moved backward %+v", stayed)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_receipts SET state='failed', failure_reason='rewrite', handed_off_at=NULL WHERE message_id=$1::uuid`, handed.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "monotonic") {
		t.Fatalf("trigger allowed rewrite: %v", err)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type='inbox.receipt_handed_off' AND after->>'message_id'=$1`, handed.ID) != 1 {
		t.Fatal("hand-off event was not written once")
	}

	retryMsg := sendCompat(t, m, w.sender, project, "grok_bot:worker", "retry-off", secret)
	respond = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`later`))}, nil
	}
	worked, err = dispatcher.DispatchOne(t.Context(), w.sender.TenantID)
	if err == nil || !worked {
		t.Fatalf("retryable failure worked=%t err=%v", worked, err)
	}
	if got := mustReceipt(t, srv, w.sender.ID, retryMsg.ID); got.State != "queued" || got.HandedOffAt != nil || got.FailureReason != "" {
		t.Fatalf("retryable failure became terminal %+v", got)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=NULL WHERE message_id=$1::uuid`, retryMsg.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	respond = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	worked, err = dispatcher.DispatchOne(t.Context(), w.sender.TenantID)
	if err != nil || !worked {
		t.Fatalf("confirm after retry worked=%t err=%v", worked, err)
	}
	if got := mustReceipt(t, srv, w.sender.ID, retryMsg.ID); got.State != "handed_off" || got.HandedOffAt == nil || got.EffectiveLevel != "simple" {
		t.Fatalf("confirmed after retry %+v", got)
	}

	failedMsg := sendCompat(t, m, w.sender, project, "grok_bot:worker", "fail-off", secret)
	if got := mustReceipt(t, srv, w.sender.ID, failedMsg.ID); got.State != "queued" {
		t.Fatalf("failure candidate %+v", got)
	}
	respond = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`no`))}, nil
	}
	worked, err = dispatcher.DispatchOne(t.Context(), w.sender.TenantID)
	if err == nil || !worked {
		t.Fatalf("terminal failure worked=%t err=%v", worked, err)
	}
	failed := mustReceipt(t, srv, w.sender.ID, failedMsg.ID)
	if failed.State != "failed" || failed.FailureReason != "http_error" || failed.HandedOffAt != nil || failed.Adapter != "grok_bot_routine" {
		t.Fatalf("failed receipt %+v", failed)
	}
	if strings.Contains(string(mustReceiptRaw(t, srv, w.sender.ID, failedMsg.ID)), secret) {
		t.Fatal("failed receipt leaked the body")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		return advanceReceipt(t.Context(), tx, w.agent, failedMsg.ID, "handed_off", "simple", "", receiptTarget{ID: &target.ID, Version: &target.Version, Adapter: target.Adapter, Address: target.Address})
	}); err != nil {
		t.Fatal(err)
	}
	if stayed := mustReceipt(t, srv, w.sender.ID, failedMsg.ID); stayed.State != "failed" || stayed.FailureReason != "http_error" || stayed.HandedOffAt != nil {
		t.Fatalf("failed moved backward %+v", stayed)
	}

	leaked := eventText(t, w.db.App, w.sender.TenantID, "inbox.receipt_queued") +
		eventText(t, w.db.App, w.sender.TenantID, "inbox.receipt_handed_off") +
		eventText(t, w.db.App, w.sender.TenantID, "inbox.receipt_failed")
	if strings.Contains(leaked, secret) || strings.Contains(leaked, senderKey) || strings.Contains(leaked, targetRef) {
		t.Fatal("receipt event leaked private material")
	}
}

// Every request in this test goes through the production auth middleware,
// route permission table, ServeMux pattern and receipt handler.
func TestReceiptThroughAuthGate(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	projectSender := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Person, "Project sender", nil)
	noGrant := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Person, "No receipt grant", nil)
	dbtest.BindRole(t, w.db, w.sender.TenantID, noGrant.ID, "viewer")
	if _, err := w.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, w.sender.TenantID, projectSender.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='member'`, w.sender.TenantID, projectSender.ID, project); err != nil {
		t.Fatal(err)
	}
	server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m}}
	if _, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.Handler())
	t.Cleanup(srv.Close)
	sender := receiptSession(t, w, w.sender)
	recipient := receiptSession(t, w, w.recipient)
	other := receiptSession(t, w, w.other)
	projectCredential := receiptSession(t, w, projectSender)
	noGrantCredential := receiptSession(t, w, noGrant)
	admin := receiptSession(t, w, w.admin)
	noReceiptKey := receiptAgentKey(t, w, w.agent, "inbox.send")
	withReceiptKey := receiptAgentKey(t, w, w.agent, "inbox.send", "inbox.receipt")

	sendBody := sendJSON(w.recipient.ID, "direct body", "real-gate-replay", nil, nil)
	status, data := receiptRequest(t, srv, sender, http.MethodPost, "/api/inbox/messages", sendBody)
	if status != 201 {
		t.Fatalf("send: %d %s", status, data)
	}
	direct := mustJSON[Message](t, data)
	path := "/api/inbox/messages/" + direct.ID + "/receipt"
	status, data = receiptRequest(t, srv, sender, http.MethodGet, path, "")
	if status != 200 || mustJSON[Receipt](t, data).State != "queued" {
		t.Fatalf("sender receipt: %d %s", status, data)
	}
	_, absent := receiptRequest(t, srv, sender, http.MethodGet, "/api/inbox/messages/00000000-0000-4000-8000-000000000000/receipt", "")
	for _, credential := range []string{recipient, other, admin, projectCredential, noGrantCredential, noReceiptKey} {
		status, data = receiptRequest(t, srv, credential, http.MethodGet, path, "")
		if status != 404 || string(data) != string(absent) {
			t.Fatalf("non-sender receipt: %d %s", status, data)
		}
	}

	// Concurrent exact replays all resolve to one durable message and receipt.
	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/inbox/messages", strings.NewReader(sendBody))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.AddCookie(&http.Cookie{Name: "aeon_session", Value: sender})
			resp, err := srv.Client().Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			results <- result{status: resp.StatusCode, body: body, err: err}
		}()
	}
	wg.Wait()
	close(results)
	for replay := range results {
		if replay.err != nil || replay.status != 201 || mustJSON[Message](t, replay.body).ID != direct.ID {
			t.Fatalf("concurrent replay: %d %s %v", replay.status, replay.body, replay.err)
		}
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM inbox_receipts WHERE message_id=$1::uuid`, direct.ID) != 1 {
		t.Fatal("concurrent replay duplicated receipt")
	}
	status, data = receiptRequest(t, srv, recipient, http.MethodPost, "/api/inbox/messages/"+direct.ID+"/ack", "")
	if status != 200 {
		t.Fatalf("direct ack: %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, sender, http.MethodGet, path, "")
	confirmed := mustJSON[Receipt](t, data)
	if status != 200 || confirmed.State != "handed_off" || confirmed.HandedOffAt == nil || confirmed.EffectiveLevel != "" {
		t.Fatalf("direct confirmation: %d %+v", status, confirmed)
	}
	status, data = receiptRequest(t, srv, recipient, http.MethodPost, "/api/inbox/messages/"+direct.ID+"/ack", "")
	if status != 200 {
		t.Fatalf("repeat ack: %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, sender, http.MethodGet, path, "")
	if after := mustJSON[Receipt](t, data); status != 200 || after.HandedOffAt == nil || *after.HandedOffAt != *confirmed.HandedOffAt {
		t.Fatalf("direct receipt moved on replay: %d %+v", status, after)
	}

	// A scoped agent can read its own receipt. The same principal's send-only
	// key gets the indistinguishable 404, proving the key scope is enforced.
	status, data = receiptRequest(t, srv, noReceiptKey, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "agent body", "agent-receipt", nil, nil))
	if status != 201 {
		t.Fatalf("agent send: %d %s", status, data)
	}
	agentMessage := mustJSON[Message](t, data)
	agentPath := "/api/inbox/messages/" + agentMessage.ID + "/receipt"
	status, data = receiptRequest(t, srv, noReceiptKey, http.MethodGet, agentPath, "")
	if status != 404 || string(data) != string(absent) {
		t.Fatalf("agent without scope: %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, withReceiptKey, http.MethodGet, agentPath, "")
	if status != 200 || mustJSON[Receipt](t, data).State != "queued" {
		t.Fatalf("agent with scope: %d %s", status, data)
	}

	// Project-only grant resolves the sender's compat message project. Only
	// the managed adapter's confirmed delivery advances this receipt.
	const targetSecret = "fixture-target-secret"
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{
		Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook",
		Ref: "https://8.8.8.8/receipt-fixture", Secret: targetSecret, Role: "primary", MaximumLevel: "simple",
	}); err != nil {
		t.Fatal(err)
	}
	compat := sendCompat(t, m, projectSender, project, "grok_bot:worker", "project-gate", "managed body")
	compatPath := "/api/inbox/messages/" + compat.ID + "/receipt"
	status, data = receiptRequest(t, srv, projectCredential, http.MethodGet, compatPath, "")
	if status != 200 || mustJSON[Receipt](t, data).State != "queued" {
		t.Fatalf("project sender: %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, recipient, http.MethodGet, compatPath, "")
	if status != 404 || string(data) != string(absent) {
		t.Fatalf("managed recipient read: %d %s", status, data)
	}
	dispatcher := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	worked, err := dispatcher.DispatchOne(t.Context(), w.sender.TenantID)
	if err != nil || !worked {
		t.Fatalf("adapter confirmation: %t %v", worked, err)
	}
	status, data = receiptRequest(t, srv, projectCredential, http.MethodGet, compatPath, "")
	managed := mustJSON[Receipt](t, data)
	if status != 200 || managed.State != "handed_off" || managed.HandedOffAt == nil {
		t.Fatalf("managed confirmation: %d %+v", status, managed)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		return advanceReceipt(t.Context(), tx, w.agent, compat.ID, "failed", "", "late", receiptTarget{})
	}); err != nil {
		t.Fatal(err)
	}
	status, data = receiptRequest(t, srv, projectCredential, http.MethodGet, compatPath, "")
	if after := mustJSON[Receipt](t, data); status != 200 || after.State != "handed_off" || after.HandedOffAt == nil || *after.HandedOffAt != *managed.HandedOffAt {
		t.Fatalf("terminal receipt moved: %d %+v", status, after)
	}

	// A pre-receipt message must not acquire a made-up queued state.
	status, data = receiptRequest(t, srv, sender, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "old body", "old-no-receipt", nil, nil))
	if status != 201 {
		t.Fatalf("old fixture send: %d %s", status, data)
	}
	old := mustJSON[Message](t, data)
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM inbox_receipts WHERE message_id=$1::uuid`, old.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, data = receiptRequest(t, srv, sender, http.MethodGet, "/api/inbox/messages/"+old.ID+"/receipt", "")
	if status != 404 || string(data) != string(absent) {
		t.Fatalf("historical message: %d %s", status, data)
	}
}

func receiptSession(t *testing.T, w *world, p tenant.Principal) string {
	t.Helper()
	var identity string
	if err := w.db.Admin.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('receipt-test',$1) RETURNING id::text`, p.ID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE principals SET identity_id=$1::uuid WHERE tenant_id=$2::uuid AND id=$3::uuid`, identity, p.TenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	raw := sha256.Sum256([]byte("session/" + p.ID))
	id := sha256.Sum256(raw[:])
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES($1,$2::uuid,$3::uuid,$4::uuid,now()+interval '1 day')`, hex.EncodeToString(id[:]), identity, p.TenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}

func receiptAgentKey(t *testing.T, w *world, p tenant.Principal, scopes ...string) string {
	t.Helper()
	keyID := sha256.Sum256([]byte(strings.Join(scopes, "/") + p.ID))
	secretBytes := sha256.Sum256([]byte("secret/" + strings.Join(scopes, "/") + p.ID))
	secret := hex.EncodeToString(secretBytes[:])
	prefix := strings.ReplaceAll(p.TenantID, "-", "") + hex.EncodeToString(keyID[:8])
	hash := sha256.Sum256([]byte(secret))
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1::uuid,$2::uuid,'receipt-test',$3,$4,$5,(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, p.TenantID, p.ID, prefix, hex.EncodeToString(hash[:]), scopes); err != nil {
		t.Fatal(err)
	}
	return "aeon_" + prefix + "_" + secret
}

func receiptRequest(t *testing.T, srv *httptest.Server, credential, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(credential, "aeon_") {
		req.Header.Set("Authorization", "Bearer "+credential)
	} else {
		req.AddCookie(&http.Cookie{Name: "aeon_session", Value: credential})
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func sendCompat(t *testing.T, m *messaging, p tenant.Principal, project, to, key, body string) CompatMessage {
	t.Helper()
	in := compatInput(to, key)
	in.Body = body
	return mustCompatSend(t, m, p, project, in)
}

func mustReceipt(t *testing.T, srv *httptest.Server, principal, id string) Receipt {
	t.Helper()
	return mustJSON[Receipt](t, mustReceiptRaw(t, srv, principal, id))
}

func mustReceiptRaw(t *testing.T, srv *httptest.Server, principal, id string) []byte {
	t.Helper()
	status, body := do(t, srv, principal, http.MethodGet, "/api/inbox/messages/"+id+"/receipt", "", nil)
	if status != 200 {
		t.Fatalf("receipt %s %d %s", id, status, body)
	}
	return body
}
