package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/secretvault"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHarnessNativeMessageReauthorizesClosedRoute(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	openChangesTestDB(t)
	projectID := seedChangesProject(t, "NAT")
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status,role_key,is_super_admin) VALUES('native-editor','disabled','external','active','external',0)`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, userID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker'),(?,'peer')`, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	credentialID := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id) VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`, uuid.NewString(), userID, credentialID); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "test", SessionRef: "owned", WorkerLease: handlerWorkerLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerOwned, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE harness_sessions SET phase='working',heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	send := func(body, lease, agent string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", fmt.Sprint(projectID))
		route.URLParams.Add("sessionID", session.ID)
		r = r.WithContext(context.WithValue(auth.WithPrincipal(r.Context(), principal), chi.RouteCtxKey, route))
		r.Header.Set(AgentNameHeader, agent)
		r.Header.Set(harnessWorkerLeaseHeader, lease)
		r.Header.Set("Idempotency-Key", uuid.NewString())
		w := httptest.NewRecorder()
		sendHarnessMessage(w, r)
		return w
	}
	body := `{"to":"claude:peer","body":"status observation","reply_to":"","is_action_request":false,"expects_reply":false}`
	w := send(body, handlerWorkerLease, "worker")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"message_id"`) || strings.Contains(w.Body.String(), "status observation") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		body, lease, agent string
		status             int
	}{
		{body, "", "worker", 403}, {body, handlerWorkerLease, "peer", 403}, {strings.TrimSuffix(body, "}") + `,"sender":"peer"}`, handlerWorkerLease, "worker", 400},
	} {
		if w := send(tc.body, tc.lease, tc.agent); w.Code != tc.status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
		}
	}
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='viewer' WHERE user_id=? AND project_id=?`, userID, projectID); err != nil {
		t.Fatal(err)
	}
	if w := send(body, handlerWorkerLease, "worker"); w.Code != 403 {
		t.Fatalf("demoted editor status=%d", w.Code)
	}
	if _, err := db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, credentialID); err != nil {
		t.Fatal(err)
	}
	if w := send(body, handlerWorkerLease, "worker"); w.Code != 401 {
		t.Fatalf("revoked principal status=%d", w.Code)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
}
