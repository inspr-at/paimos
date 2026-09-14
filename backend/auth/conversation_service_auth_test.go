// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
)

func insertConversationServiceAuthBinding(t *testing.T, rawKey string) (int64, int64) {
	t.Helper()
	userID := insertPrincipalUser(t, "conversation-service-routes")
	projectResult, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Conversation auth','CAT','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	keyID := insertMachineNotifierAuthKey(t, userID, rawKey, "general", "", nil)
	_, err = db.DB.Exec(`INSERT INTO conversation_service_bindings(
		binding_id,revision,api_key_id,project_id,host_id,project_ref,runtime_id,runtime_generation,
		account_label,account_key,attachment_revision,dispatch_profile_id,dispatch_profile_version,
		execution_policy_id,max_input_bytes,max_messages,max_output_bytes,max_event_bytes,max_events,
		max_timeout_ms,created_by,session_credential_id,created_at)
		VALUES(?,1,?,?,?,?,?,?,?,?,7,'codex-sol-high','1','aithema-conversation-v1',
		131072,128,262144,8192,512,180000,?,?,'2026-09-14T12:00:00.000Z')`,
		uuid.NewString(), keyID, projectID, "aithema-host", "aithema-project", uuid.NewString(), uuid.NewString(),
		"chatgpt", "acct-main", userID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return userID, keyID
}

func TestConversationServiceCredentialHasDedicatedPrincipalAndRouteAllowlist(t *testing.T) {
	setupPrincipalTestDB(t)
	rawKey := "paimos_test_conversation_service_routes"
	userID, keyID := insertConversationServiceAuthBinding(t, rawKey)
	user, principal, err := ResolveAPIKeyPrincipal(rawKey)
	if err != nil || user.ID != userID || principal.Kind() != PrincipalConversationService || principal.APIKeyID() != keyID || len(principal.Scopes()) != 0 {
		t.Fatalf("dedicated principal user=%+v principal=%+v err=%v", user, principal, err)
	}

	tests := []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodPost, "/api/projects/1/conversation/v1/calls", true},
		{http.MethodGet, "/api/projects/1/conversation/v1/calls/one", true},
		{http.MethodGet, "/api/projects/1/conversation/v1/calls/one/events", true},
		{http.MethodPost, "/api/projects/1/conversation/v1/calls/one/cancel", true},
		{http.MethodGet, "/api/projects/1/conversation/v1/calls", false},
		{http.MethodPost, "/api/projects/1/conversation/v1/calls/one", false},
		{http.MethodPost, "/api/projects/1/conversation/v1/calls/one/events", false},
		{http.MethodGet, "/api/projects/1/conversation/v1/calls/one/cancel", false},
		{http.MethodPost, "/api/projects/1/runtimes/runtime/conversation/v1/claim", false},
		{http.MethodPost, "/api/auth/conversation-services", false},
		{http.MethodGet, "/api/auth/me", false},
		{http.MethodGet, "/api/projects/1/conversation/v1/calls/one/events/more", false},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			reached := false
			handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+rawKey)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if test.allowed && (recorder.Code != http.StatusNoContent || !reached) {
				t.Fatalf("allowed route status=%d reached=%v", recorder.Code, reached)
			}
			if !test.allowed && (recorder.Code != http.StatusForbidden || reached) {
				t.Fatalf("denied route status=%d reached=%v", recorder.Code, reached)
			}
		})
	}

	if _, err := db.DB.Exec(`UPDATE api_keys SET disabled_at='2026-09-14T12:01:00.000Z' WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveAPIKeyPrincipal(rawKey); err == nil {
		t.Fatal("disabled conversation-service credential authenticated")
	}
}
