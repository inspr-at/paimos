// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// These sentinels must never run for the narrow key. Registered real route
// permissions go through production auth; a 404 from an unmounted route cannot
// accidentally satisfy the denied-access assertions.
type messagingOnlySentinels struct{ t *testing.T }

func (m messagingOnlySentinels) Mount(mux *http.ServeMux) {
	for _, route := range []string{"GET /api/kinds", "GET /api/nodes", "GET /api/nodes/{nodeId}", "GET /api/projects/lookup", "GET /api/runs", "POST /api/nodes", "POST /api/runs"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			m.t.Errorf("messaging-only key reached %s", r.Pattern)
			w.WriteHeader(http.StatusOK)
		})
	}
}

func messagingOnlyServer(t *testing.T, w *world, m *messaging, middleware ...func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m, messagingOnlySentinels{t}}}
	if _, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)}); err != nil {
		t.Fatal(err)
	}
	server.Middleware = append(server.Middleware, middleware...)
	srv := httptest.NewTLSServer(server.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func messagingOnlyKey(t *testing.T, w *world) (string, string, string) {
	t.Helper()
	expires := time.Now().Add(7 * 24 * time.Hour)
	keyID, principalID, credential, err := auth.OperatorCreateAgentKey(t.Context(), w.db.App, w.sender.TenantID, "amy", "", []string{"inbox.read", "inbox.send", "inbox.receipt"}, &expires, w.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	return keyID, principalID, credential
}

type messagingAdmissionBody struct {
	io.ReadCloser
	once  sync.Once
	pause func()
}

func (b *messagingAdmissionBody) Read(p []byte) (int, error) {
	b.once.Do(b.pause)
	return b.ReadCloser.Read(p)
}

// Risk: granting only messaging silently needs node/project authority, leaks
// other inboxes/receipts, or lets an unpaired assistant mutate unrelated data.
func TestMessagingOnlyAgentThroughProductionAuth(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	keyID, amyID, amy := messagingOnlyKey(t, w)
	srv := messagingOnlyServer(t, w, m)
	peer := receiptSession(t, w, w.recipient)
	other := receiptSession(t, w, w.other)
	var fullAccess bool
	var scopes []string
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT coalesce(full_access,false),scopes FROM agent_keys WHERE id=$1`, keyID).Scan(&fullAccess, &scopes); err != nil {
		t.Fatal(err)
	}
	slices.Sort(scopes)
	if fullAccess || len(scopes) != 3 || strings.Join(scopes, ",") != "inbox.read,inbox.receipt,inbox.send" {
		t.Fatalf("fixture widened key ceiling: full_access=%v scopes=%v", fullAccess, scopes)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM role_bindings b JOIN role_permissions rp ON rp.tenant_id=b.tenant_id AND rp.role_id=b.role_id WHERE b.principal_id=$1 AND rp.permission='nodes.read'`, amyID) != 0 {
		t.Fatal("messaging principal unexpectedly has node visibility")
	}
	status, data := receiptRequest(t, srv, amy, "POST", "/api/inbox/messages", sendJSON(w.recipient.ID, "outgoing", "amy-outgoing", nil, nil))
	if status != 201 {
		t.Fatalf("send %d %s", status, data)
	}
	sent := mustJSON[Message](t, data)
	if sent.SenderPrincipalID != amyID || sent.RecipientSessionID != nil || sent.SenderSessionID != nil {
		t.Fatal("ordinary agent send gained a session dependency")
	}
	receiptPath := "/api/inbox/messages/" + sent.ID + "/receipt"
	status, data = receiptRequest(t, srv, amy, "GET", receiptPath, "")
	if status != 200 || mustJSON[Receipt](t, data).State != "queued" {
		t.Fatalf("own queued receipt %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, other, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 200 || len(mustJSON[Page](t, data).Items) != 0 {
		t.Fatalf("other principal saw message %d %s", status, data)
	}
	status, _ = receiptRequest(t, srv, other, "POST", "/api/inbox/messages/"+sent.ID+"/ack", "")
	if status != 404 {
		t.Fatalf("non-recipient ack %d", status)
	}
	status, data = receiptRequest(t, srv, peer, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 200 || len(mustJSON[Page](t, data).Items) != 1 {
		t.Fatalf("peer poll %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, peer, "POST", "/api/inbox/messages/"+sent.ID+"/ack", "")
	if status != 200 {
		t.Fatalf("peer ack %d %s", status, data)
	}
	status, data = receiptRequest(t, srv, amy, "GET", receiptPath, "")
	if status != 200 || mustJSON[Receipt](t, data).State != "handed_off" {
		t.Fatalf("receiver-confirmed receipt %d %s", status, data)
	}

	// LISTEN has completed and the empty page was read before the peer sends.
	waiting := make(chan struct{}, 1)
	m.base.waitNotify = func(ctx context.Context, conn *pgx.Conn, tenantID string, deadline time.Time) error {
		select {
		case waiting <- struct{}{}:
		default:
		}
		return waitTenantNotify(ctx, conn, tenantID, deadline)
	}
	type result struct {
		status int
		body   []byte
	}
	polled := make(chan result, 1)
	guard, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	go func() {
		status, data := receiptRequest(t, srv, amy, "GET", "/api/inbox/messages?after=0&limit=10&wait_ms=25000", "")
		polled <- result{status, data}
	}()
	dbtest.Await(t, guard, waiting)
	status, data = receiptRequest(t, srv, peer, "POST", "/api/inbox/messages", sendJSON(amyID, "incoming", "amy-incoming", &sent.ID, nil))
	if status != 201 {
		t.Fatalf("peer send %d %s", status, data)
	}
	incoming := mustJSON[Message](t, data)
	page := dbtest.Await(t, guard, polled)
	if page.status != 200 {
		t.Fatalf("long poll %d %s", page.status, page.body)
	}
	items := mustJSON[Page](t, page.body).Items
	if len(items) != 1 || items[0].ID != incoming.ID || items[0].ReplyToID == nil || *items[0].ReplyToID != sent.ID || items[0].RecipientSessionID != nil {
		t.Fatalf("wrong long-poll handoff: %+v", items)
	}
	for range 2 {
		status, data = receiptRequest(t, srv, amy, "POST", "/api/inbox/messages/"+incoming.ID+"/ack", "")
		if status != 200 || mustJSON[Message](t, data).AckedAt == nil {
			t.Fatalf("own idempotent ack %d %s", status, data)
		}
	}
	status, data = receiptRequest(t, srv, amy, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 200 || len(mustJSON[Page](t, data).Items) != 0 {
		t.Fatalf("ack did not drain inbox %d %s", status, data)
	}
	status, _ = receiptRequest(t, srv, amy, "GET", "/api/inbox/messages/"+incoming.ID+"/receipt", "")
	if status != 404 {
		t.Fatalf("another sender's receipt %d", status)
	}
	status, _ = receiptRequest(t, srv, amy, "POST", "/api/inbox/messages/"+sent.ID+"/ack", "")
	if status != 403 {
		t.Fatalf("sender consumed recipient inbox %d", status)
	}
	for _, path := range []string{"/api/kinds", "/api/nodes", "/api/nodes/" + project, "/api/projects/lookup?key=MSG", "/api/settings/inbox-delivery", "/api/agent-keys", "/api/runs", "/api/projects/" + project + "/messages"} {
		status, data = receiptRequest(t, srv, amy, "GET", path, "")
		if status != 403 {
			t.Fatalf("out-of-scope read %s: %d %s", path, status, data)
		}
	}
	// The key's inbox scope never opens project visibility, even by UUID.
	status, _ = receiptRequest(t, srv, amy, "GET", "/api/projects/"+project+"/messages/listen", "")
	if status != 404 {
		t.Fatalf("invisible project listen %d", status)
	}
	for _, path := range []string{"/api/nodes", "/api/runs", "/api/agent-keys"} {
		status, data = receiptRequest(t, srv, amy, "POST", path, `{}`)
		if status != 403 {
			t.Fatalf("out-of-scope write %s: %d %s", path, status, data)
		}
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type='inbox.sent'`) != 2 || countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type='inbox.acked'`) != 2 {
		t.Fatal("send/ack audit missing or duplicate ack appended another event")
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, keyID); err != nil {
		t.Fatal(err)
	}
	status, _ = receiptRequest(t, srv, amy, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 401 {
		t.Fatalf("expired key authenticated: %d", status)
	}
	if err := auth.OperatorRevokeAgentKey(t.Context(), w.db.App, w.sender.TenantID, keyID); err != nil {
		t.Fatal(err)
	}
	status, _ = receiptRequest(t, srv, amy, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 401 {
		t.Fatalf("revoked key authenticated: %d", status)
	}
	t.Run("existing project-only harness messaging", func(t *testing.T) {
		w, m, project, _ := messagingWorld(t)
		projectOnly(t, w, w.sender, project)
		projectOnly(t, w, w.agent, project)
		session := messageTestSession(t, w, project, w.agent, "Existing worker")
		srv := messagingOnlyServer(t, w, m)
		sender := receiptSession(t, w, w.sender)
		recipient := receiptAgentKey(t, w, w.agent, "inbox.send", "inbox.read")
		path := "/api/projects/" + project + "/messages"
		body := fmt.Sprintf(`{"to":%q,"body":"bound fixture","idempotency_key":"bound-fixture","recipient_session_id":%q,"delivery_level":"simple"}`, w.agent.ID, session)
		status, data := receiptRequest(t, srv, sender, "POST", path, body)
		if status != 201 {
			t.Fatalf("project-only send %d %s", status, data)
		}
		sent := mustJSON[CompatMessage](t, data)
		status, data = receiptRequest(t, srv, recipient, "GET", path+"/listen?session="+session, "")
		if status != 200 || len(mustJSON[compatPage](t, data).Items) != 1 {
			t.Fatalf("project-only session listen %d %s", status, data)
		}
		status, data = receiptRequest(t, srv, recipient, "POST", path+"/"+sent.ID+"/ack", "")
		if status != 200 {
			t.Fatalf("project-only session ack %d %s", status, data)
		}
	})
}

// Risk: admission succeeds but rights change before the final mutation. Pause
// behind production admission, revoke inbox.send under the shared tenant fence,
// and require refusal without a message/ack/receipt/audit write.
func TestInboxFinalWriteRechecksMessagingPermission(t *testing.T) {
	for _, operation := range []string{"send", "ack", "project send"} {
		t.Run(operation, func(t *testing.T) {
			w, m, project, _ := messagingWorld(t)
			_, amyID, amy := messagingOnlyKey(t, w)
			path, body, credential, actorID := "/api/inbox/messages", sendJSON(w.recipient.ID, "must not commit", "revoked-send", nil, nil), amy, amyID
			var ackID string
			if operation == "ack" {
				msg, err := m.base.send(t.Context(), w.sender, sendInput{Recipient: amyID, Body: "pending", Key: "pending"})
				if err != nil {
					t.Fatal(err)
				}
				ackID, path, body = msg.ID, "/api/inbox/messages/"+msg.ID+"/ack", ""
			}
			if operation == "project send" {
				// Existing project-only grants remain sufficient; this check must
				// use the addressed project's grant, not require workspace access.
				projectOnly(t, w, w.sender, project)
				credential, actorID = receiptSession(t, w, w.sender), w.sender.ID
				path = "/api/projects/" + project + "/messages"
				body = fmt.Sprintf(`{"to":%q,"body":"must not commit","idempotency_key":"revoked-project","delivery_level":"simple"}`, w.recipient.ID)
			}
			admitted, resume := make(chan struct{}), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(resume) })
			srv := messagingOnlyServer(t, w, m, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" && r.URL.Path == path {
						if p, ok := tenant.PrincipalFrom(r.Context()); !ok || p.ID != actorID {
							t.Error("barrier was reached without authenticated identity")
						}
						pause := func() {
							close(admitted)
							select {
							case <-resume:
							case <-r.Context().Done():
							}
						}
						if operation == "ack" {
							pause()
						} else {
							// Decode follows both project visibility and the
							// legacy bearer precheck, but precedes final write.
							r.Body = &messagingAdmissionBody{ReadCloser: r.Body, pause: pause}
						}
					}
					next.ServeHTTP(rw, r)
				})
			})
			before := countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events`)
			guard, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			type result struct {
				status int
				body   []byte
			}
			finished := make(chan result, 1)
			go func() {
				status, data := receiptRequest(t, srv, credential, "POST", path, body)
				finished <- result{status, data}
			}()
			dbtest.Await(t, guard, admitted)
			if err := db.InTenant(dbtest.Seed(guard), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
				if err := db.LockTenant(guard, tx, w.sender.TenantID); err != nil {
					return err
				}
				if operation == "project send" {
					_, err := tx.Exec(guard, `DELETE FROM role_bindings WHERE principal_id=$1`, actorID)
					return err
				}
				_, err := tx.Exec(guard, `DELETE FROM role_permissions WHERE permission='inbox.send' AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1)`, actorID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(resume) })
			out := dbtest.Await(t, guard, finished)
			if out.status != 403 || !strings.Contains(string(out.body), "forbidden") {
				t.Fatalf("revoked mutation %d %s", out.status, out.body)
			}
			if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events`) != before {
				t.Fatal("revoked mutation appended audit or receipt event")
			}
			if operation == "ack" {
				if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM inbox_messages WHERE id=$1 AND acked_at IS NULL`, ackID) != 1 {
					t.Fatal("revoked ack consumed pending message")
				}
			} else if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM inbox_messages`) != 0 {
				t.Fatal("revoked send persisted a message")
			}
		})
	}
}
