// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

func TestHeldActionResolutionHTTPIsHumanOnlyAndIdempotent(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "PAIMOS", "PAI")
	var issueID int64
	issue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,905,'ticket','Human resolution UI','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ = issue.LastInsertId()
	for _, name := range []string{"sender", "codex", "amy"} {
		if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	var orchestratorAgentID, adminUserID int64
	if err := db.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&orchestratorAgentID); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,
		updated_by_user_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, orchestratorAgentID, adminUserID); err != nil {
		t.Fatal(err)
	}
	service := agentmessage.NewService(db.DB)
	if _, err := service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "01a069e0-f6b5-70d1-a487-02d1d00f1019",
		MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 0 {
		t.Fatalf("bootstrap attention projection=%d err=%v", inserted, err)
	}
	held, err := service.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "human decision",
		IssueID: &issueID, ActionRequest: true, IdempotencyKey: "handler-held-action",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		var maxMessage, heldCursor, attentionRows int64
		_ = db.DB.QueryRow(`SELECT COALESCE(MAX(id),0) FROM agent_messages`).Scan(&maxMessage)
		_ = db.DB.QueryRow(`SELECT source_row_id FROM agent_attention_projection_cursors WHERE receiver_project_agent_id=? AND source_kind='held_agent_message'`, orchestratorAgentID).Scan(&heldCursor)
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items`).Scan(&attentionRows)
		t.Fatalf("held attention projection=%d err=%v max_message=%d cursor=%d rows=%d held=%#v", inserted, err, maxMessage, heldCursor, attentionRows, held)
	}
	attentionBefore, err := service.ListAttention(context.Background(), agentmessage.AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy",
	})
	if err != nil || len(attentionBefore.Items) != 1 || attentionBefore.Items[0].SourceID != held.MessageID {
		t.Fatalf("held attention=%#v err=%v", attentionBefore, err)
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

	attentionAfter, err := service.ListAttention(context.Background(), agentmessage.AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy",
	})
	if err != nil || len(attentionAfter.Items) != 0 {
		t.Fatalf("resolved request remained actionable: %#v err=%v", attentionAfter, err)
	}
	var delivered int
	var heldReason, partsJSON string
	if err := db.DB.QueryRow(`SELECT delivered,held_reason,parts_json FROM agent_messages WHERE message_id=?`, held.MessageID).
		Scan(&delivered, &heldReason, &partsJSON); err != nil {
		t.Fatal(err)
	}
	if delivered != 0 || heldReason != held.HeldReason || partsJSON != `[{"kind":"text","text":"human decision"}]` {
		t.Fatalf("resolution mutated held row: delivered=%d held_reason=%q parts=%q", delivered, heldReason, partsJSON)
	}
	listResponse := ts.get(t, "/api/issues/"+itoa(issueID)+"/messages", ts.adminCookie)
	assertStatus(t, listResponse, http.StatusOK)
	var listed []map[string]any
	decode(t, listResponse, &listed)
	if len(listed) != 1 || listed[0]["human_resolution_outcome"] != "resolved" {
		t.Fatalf("resolved issue messages=%#v", listed)
	}
	serialized, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"actor_user_id", "actor_session_id", "resolution_id", "idempotency"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("issue projection leaked %q: %s", forbidden, serialized)
		}
	}
}
