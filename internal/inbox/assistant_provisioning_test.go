// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// Risk: provisioning works in the store but production authentication rejects
// its no-role identity, leaks the secret, omits owner notice/audit or lets the
// assistant use shared inbox scopes on unrelated routes.
func TestMessagingAssistantProvisionedThroughProductionAuth(t *testing.T) {
	w, m, _, _ := messagingWorld(t)
	server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m, authz.New(w.db.App), messagingOnlySentinels{t}}}
	a, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.AssistantNoticeTx = MessagingAssistantNoticeTx
	srv := httptest.NewTLSServer(server.Handler())
	t.Cleanup(srv.Close)
	owner := receiptSession(t, w, w.admin)
	expires := time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	body := fmt.Sprintf(`{"name":"Amy provisioned","expires_at":%q}`, expires.Format(time.RFC3339Nano))
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	status, data := receiptRequest(t, srv, owner, "POST", "/api/agents/assistants", body)
	if status != 201 {
		t.Fatalf("provision status %d", status)
	}
	var created struct {
		ID          string    `json:"id"`
		PrincipalID string    `json:"principal_id"`
		Token       string    `json:"token"`
		Scopes      []string  `json:"scopes"`
		ExpiresAt   time.Time `json:"expires_at"`
		Owner       string    `json:"created_by_principal_id"`
	}
	if json.Unmarshal(data, &created) != nil || created.Token == "" || !slices.Equal(created.Scopes, authz.MessagingAssistantScopes()) || !created.ExpiresAt.Equal(expires) || created.Owner != w.admin.ID {
		t.Fatal("incorrect assistant response")
	}
	if bytes.Count(data, []byte(created.Token)) != 1 {
		t.Fatal("one-time response repeats secret")
	}
	// Existing Agents/key UI gets the metadata without any new presentation.
	status, data = receiptRequest(t, srv, owner, "GET", "/api/agent-keys", "")
	if status != 200 || bytes.Contains(data, []byte(created.Token)) {
		t.Fatal("owner metadata lacks scope/expiry or repeats token")
	}
	var listed struct {
		Keys []struct {
			ID        string    `json:"id"`
			Scopes    []string  `json:"scopes"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"keys"`
	}
	if json.Unmarshal(data, &listed) != nil {
		t.Fatal("invalid owner key metadata")
	}
	found := false
	for _, key := range listed.Keys {
		if key.ID == created.ID {
			found = slices.Equal(key.Scopes, authz.MessagingAssistantScopes()) && key.ExpiresAt.Equal(expires)
		}
	}
	if !found {
		t.Fatal("owner metadata lacks assistant scope/expiry")
	}
	status, data = receiptRequest(t, srv, owner, "GET", "/api/members", "")
	if status != 200 || !bytes.Contains(data, []byte(created.PrincipalID)) {
		t.Fatal("assistant absent from Agents directory")
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events WHERE type='messaging_assistant.provisioned' AND after->>'caller_id'=$1 AND after->>'owner_id'=$1 AND after->>'principal_id'=$2`, w.admin.ID, created.PrincipalID) != 1 {
		t.Fatal("owner-bound audit missing")
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_messages m JOIN principals s ON s.id=m.sender_principal_id WHERE m.recipient_principal_id=$1 AND s.name='System' AND m.body LIKE '%No action needed.%' AND m.body LIKE '%'||$2||'%'`, w.admin.ID, created.PrincipalID) != 1 {
		t.Fatal("System FYI notice missing")
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events WHERE id=(SELECT max(id) FROM events) AND type='messaging_assistant.provisioned'`) != 1 {
		t.Fatal("audit was not the last append")
	}
	secret := strings.Split(created.Token, "_")[2]
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM agent_keys WHERE id=$1 AND (hash=$2 OR hash=$3 OR length(hash)<>64)`, created.ID, created.Token, secret) != 0 {
		t.Fatal("key stored in plaintext")
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events WHERE coalesce(after::text,'') LIKE '%'||$1||'%' OR coalesce(before::text,'') LIKE '%'||$1||'%'`, secret) != 0 || countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_messages WHERE body LIKE '%'||$1||'%'`, secret) != 0 {
		t.Fatal("secret persisted in audit or notice")
	}
	status, data = receiptRequest(t, srv, created.Token, "POST", "/api/inbox/messages", sendJSON(w.recipient.ID, "assistant message", "provisioned-send", nil, nil))
	if status != 201 {
		t.Fatalf("no-role assistant send status %d", status)
	}
	sent := mustJSON[Message](t, data)
	status, _ = receiptRequest(t, srv, created.Token, "GET", "/api/inbox/messages/"+sent.ID+"/receipt", "")
	if status != 200 {
		t.Fatalf("assistant receipt status %d", status)
	}
	status, _ = receiptRequest(t, srv, created.Token, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 200 {
		t.Fatalf("assistant listen status %d", status)
	}
	// Widen both the key and bindings deliberately. The principal marker must
	// still deny scope-free identity/status routes and every non-inbox route.
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET full_access=true,scopes='{}' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='admin'`, w.admin.TenantID, created.PrincipalID); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{"GET", "/api/me"}, {"GET", "/api/status/help"}, {"GET", "/api/nodes"}, {"POST", "/api/agents/assistants"}, {"POST", "/api/members/agents"}, {"POST", "/api/agent-keys"}, {"PATCH", "/api/agent-keys/" + created.ID + "/scopes"}, {"POST", "/api/auth/logout"}} {
		status, data = receiptRequest(t, srv, created.Token, route.method, route.path, body)
		if status != 403 {
			t.Fatalf("marker bypass %s %s: %d", route.method, route.path, status)
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("denial leaked secret")
		}
	}
	// Owner renewal returns a new secret, refuses the old one, and keeps scope.
	status, data = receiptRequest(t, srv, owner, "POST", "/api/agents/assistants/"+created.PrincipalID+"/rotate", fmt.Sprintf(`{"expires_at":%q}`, expires.Format(time.RFC3339Nano)))
	if status != 201 {
		t.Fatalf("rotation status %d", status)
	}
	var replacement struct {
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	if json.Unmarshal(data, &replacement) != nil || replacement.Token == "" || replacement.Token == created.Token || !slices.Equal(replacement.Scopes, authz.MessagingAssistantScopes()) {
		t.Fatal("rotation response failed")
	}
	status, _ = receiptRequest(t, srv, created.Token, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 401 {
		t.Fatalf("rotated old key status %d", status)
	}
	status, _ = receiptRequest(t, srv, replacement.Token, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 200 {
		t.Fatalf("replacement status %d", status)
	}
	status, _ = receiptRequest(t, srv, owner, "DELETE", "/api/agents/assistants/"+created.PrincipalID, "")
	if status != 204 {
		t.Fatalf("owner revoke status %d", status)
	}
	status, _ = receiptRequest(t, srv, replacement.Token, "GET", "/api/inbox/messages?wait_ms=0", "")
	if status != 401 {
		t.Fatalf("revoked assistant status %d", status)
	}
	coordinator := receiptAgentKey(t, w, w.agent, authz.CoordinatorKeyScopes...)
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$2 WHERE principal_id=$1`, w.agent.ID, w.admin.ID); err != nil {
		t.Fatal(err)
	}
	status, data = receiptRequest(t, srv, coordinator, "POST", "/api/agents/assistants", strings.Replace(body, "Amy provisioned", "Coordinator helper", 1))
	if status != 201 {
		t.Fatalf("bounded coordinator HTTP status %d", status)
	}
	var bounded struct {
		Token string `json:"token"`
		Owner string `json:"created_by_principal_id"`
	}
	if json.Unmarshal(data, &bounded) != nil || bounded.Token == "" || bounded.Owner != w.admin.ID {
		t.Fatal("bounded HTTP ownership lost")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), replacement.Token) {
		t.Fatal("credential leaked into logs")
	}
	if strings.Contains(logs.String(), bounded.Token) {
		t.Fatal("bounded provisioning secret logged")
	}
}

// Risk: a coordinator passes route admission, but the owner's provisioning
// rights disappear before body decode and the final write. The barrier is
// behind production authentication and route authorization, without sleeps.
func TestMessagingAssistantFinalWriteRechecksOwnerRights(t *testing.T) {
	w, m, _, _ := messagingWorld(t)
	var role string
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'assistant_owner','Assistant owner') RETURNING id::text`, w.admin.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, w.admin.TenantID, role, append(authz.CoordinatorKeyScopes, "agents.create", "keys.manage")); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, w.admin.ID, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	coordinator := receiptAgentKey(t, w, w.agent, authz.CoordinatorKeyScopes...)
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$2 WHERE principal_id=$1`, w.agent.ID, w.admin.ID); err != nil {
		t.Fatal(err)
	}
	server := &httpapi.Server{Pool: w.db.App, Modules: []httpapi.Module{m.base, m}}
	a, err := auth.Attach(server, auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.AssistantNoticeTx = MessagingAssistantNoticeTx
	admitted, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	server.Middleware = append(server.Middleware, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if r.Pattern == "POST /api/agents/assistants" {
				r.Body = &messagingAdmissionBody{ReadCloser: r.Body, pause: func() {
					close(admitted)
					select {
					case <-resume:
					case <-r.Context().Done():
					}
				}}
			}
			next.ServeHTTP(rw, r)
		})
	})
	srv := httptest.NewTLSServer(server.Handler())
	t.Cleanup(srv.Close)
	before := countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events`)
	result := make(chan int, 1)
	go func() {
		status, _ := receiptRequest(t, srv, coordinator, "POST", "/api/agents/assistants", fmt.Sprintf(`{"name":"Revoked during admission","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339)))
		result <- status
	}()
	guard, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	dbtest.Await(t, guard, admitted)
	if err := db.InTenant(dbtest.Seed(guard), w.db.App, w.admin.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(guard, tx, w.admin.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(guard, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='agents.create'`, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(resume) })
	if status := dbtest.Await(t, guard, result); status != 403 {
		t.Fatalf("revoked owner write status %d", status)
	}
	if countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM events`) != before || countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM principals WHERE name='Revoked during admission'`) != 0 || countSQL(t, w.db.App, w.admin.TenantID, `SELECT count(*) FROM inbox_messages`) != 0 {
		t.Fatal("revoked owner write persisted principal, notice or audit")
	}
}
