// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
)

func TestHeldActionResolutionHTTPIsHumanOnlyAndIdempotent(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "PAIMOS", "PAI")
	for _, name := range []string{"sender", "codex"} {
		if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	held, err := agentmessage.NewService(db.DB).SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "human decision",
		ActionRequest: true, IdempotencyKey: "handler-held-action",
	})
	if err != nil {
		t.Fatal(err)
	}

	postResolution := func(outcome, key, agent string) *http.Response {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"outcome": outcome})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			ts.srv.URL+"/api/projects/"+itoa(projectID)+"/messages/"+held.MessageID+"/resolution", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", ts.adminCookie)
		req.Header.Set("Idempotency-Key", key)
		if agent != "" {
			req.Header.Set("X-Paimos-Agent-Name", agent)
		}
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return response
	}

	keyResponse := ts.post(t, "/api/auth/api-keys", ts.adminCookie, map[string]string{"name": "resolution-machine"})
	assertStatus(t, keyResponse, http.StatusCreated)
	var keyView struct {
		Key string `json:"key"`
	}
	decode(t, keyResponse, &keyView)
	keyBody, _ := json.Marshal(map[string]string{"outcome": "resolved"})
	keyRequest, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		ts.srv.URL+"/api/projects/"+itoa(projectID)+"/messages/"+held.MessageID+"/resolution", bytes.NewReader(keyBody))
	keyRequest.Header.Set("Content-Type", "application/json")
	keyRequest.Header.Set("Authorization", "Bearer "+keyView.Key)
	keyRequest.Header.Set("Idempotency-Key", "api-key-resolution")
	keyResult, err := http.DefaultClient.Do(keyRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, keyResult, http.StatusForbidden)

	spoofed := postResolution("resolved", "human-resolution", "codex")
	assertStatus(t, spoofed, http.StatusForbidden)
	var resolutionCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_human_resolutions`).Scan(&resolutionCount); err != nil {
		t.Fatal(err)
	}
	if resolutionCount != 0 {
		t.Fatalf("forbidden principals committed %d human resolutions", resolutionCount)
	}
	first := postResolution("resolved", "human-resolution", "")
	assertStatus(t, first, http.StatusOK)
	var firstView agentmessage.HumanResolution
	decode(t, first, &firstView)
	replay := postResolution("resolved", "human-resolution", "")
	assertStatus(t, replay, http.StatusOK)
	var replayView agentmessage.HumanResolution
	decode(t, replay, &replayView)
	if firstView.ResolutionID == "" || replayView.ResolutionID != firstView.ResolutionID || firstView.ActorUserID == 0 ||
		!strings.HasPrefix(firstView.ActorSessionID, "session:") || strings.Contains(ts.adminCookie, firstView.ActorSessionID) {
		t.Fatalf("first=%#v replay=%#v", firstView, replayView)
	}
	conflict := postResolution("dismissed", "human-resolution", "")
	assertStatus(t, conflict, http.StatusConflict)
}
