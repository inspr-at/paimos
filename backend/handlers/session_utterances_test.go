package handlers_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

type sessionUtteranceTestResult struct {
	SchemaVersion          int     `json:"schema_version"`
	UtteranceID            string  `json:"utterance_id"`
	RouteKind              string  `json:"route_kind"`
	ProductSessionID       string  `json:"product_session_id"`
	ProductSessionRevision int64   `json:"product_session_revision"`
	MessageID              string  `json:"message_id"`
	ThreadID               string  `json:"thread_id"`
	DeliveryID             *string `json:"delivery_id"`
	CreatedAt              string  `json:"created_at"`
}

func postSessionUtteranceRaw(t *testing.T, ts *testServer, projectID int64, cookie string, body []byte, csrf bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/projects/%d/session-utterances/v1", ts.srv.URL, projectID), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.srv.URL)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
		if csrf {
			req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
		}
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func postSessionUtterance(t *testing.T, ts *testServer, projectID int64, cookie, utteranceID, text string, selection any) (*http.Response, sessionUtteranceTestResult) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"schema_version":   1,
		"utterance_id":     utteranceID,
		"text":             text,
		"selected_session": selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := postSessionUtteranceRaw(t, ts, projectID, cookie, body, true)
	var result sessionUtteranceTestResult
	if response.StatusCode == http.StatusCreated {
		decode(t, response, &result)
	}
	return response, result
}

func seedSessionUtteranceTarget(t *testing.T, projectID, agentID int64, agentName string) (string, string) {
	t.Helper()
	harnessID := seedSessionHomeHarness(t, projectID, agentID, agentName, "codex", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true}, true)
	var targetID string
	if err := db.DB.QueryRow(`SELECT message_target_id FROM harness_sessions WHERE id=?`, harnessID).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	return targetID, harnessID
}

func TestSessionUtteranceSelectedAgentIsAttributedDurableAndExactlyOnce(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "test")
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Voice route", "VRT")
	agentID := seedSessionHomeAgent(t, projectID, "worker")
	sessionID := seedSessionHomeProductSession(t, projectID, "project_agent", &agentID, nil, "Worker", "2026-08-31T10:00:00.000Z")
	targetID, _ := seedSessionUtteranceTarget(t, projectID, agentID, "worker")

	utteranceID := "utt_0123456789abcdef0123456789abcdef"
	selection := map[string]any{"product_session_id": sessionID, "revision": 1}
	response, first := postSessionUtterance(t, ts, projectID, ts.adminCookie, utteranceID, "Please inspect the build", selection)
	assertStatus(t, response, http.StatusCreated)
	if response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache policy=%q", response.Header.Get("Cache-Control"))
	}
	if first.SchemaVersion != 1 || first.UtteranceID != utteranceID || first.RouteKind != "project_agent" ||
		first.ProductSessionID != sessionID || first.ProductSessionRevision != 1 || first.ThreadID != sessionID ||
		first.DeliveryID == nil || first.MessageID == "" || first.CreatedAt == "" {
		t.Fatalf("unexpected result: %+v", first)
	}

	var fromAgent sql.NullInt64
	var fromUser, toAgent int64
	var role, body, productSessionID, fromAddress, toAddress string
	if err := db.DB.QueryRow(`SELECT from_agent_id,from_user_id,to_agent_id,role,body,product_session_id,from_address,to_address
		FROM agent_messages WHERE message_id=?`, first.MessageID).
		Scan(&fromAgent, &fromUser, &toAgent, &role, &body, &productSessionID, &fromAddress, &toAddress); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if fromAgent.Valid || fromUser != adminID || toAgent != agentID || role != "human" || body != "Please inspect the build" ||
		productSessionID != sessionID || fromAddress != fmt.Sprintf("user:%d", adminID) || toAddress != "codex:worker" {
		t.Fatalf("wrong durable attribution: fromAgent=%v fromUser=%d toAgent=%d role=%q body=%q session=%q from=%q to=%q",
			fromAgent, fromUser, toAgent, role, body, productSessionID, fromAddress, toAddress)
	}
	var deliveryMessageID int64
	var deliveryTarget, deliveryState string
	if err := db.DB.QueryRow(`SELECT message_row_id,primary_target_id,state FROM agent_message_deliveries WHERE delivery_id=?`, *first.DeliveryID).
		Scan(&deliveryMessageID, &deliveryTarget, &deliveryState); err != nil {
		t.Fatal(err)
	}
	if deliveryMessageID <= 0 || deliveryTarget != targetID || deliveryState != "pending" {
		t.Fatalf("wrong delivery row: row=%d target=%q state=%q", deliveryMessageID, deliveryTarget, deliveryState)
	}
	ledger := agentmessage.NewService(db.DB)
	envelope, err := ledger.GetEnvelope(t.Context(), projectID, first.MessageID)
	if err != nil || envelope.MessageID != first.MessageID || envelope.Role != "human" || envelope.From != fmt.Sprintf("user:%d", adminID) {
		t.Fatalf("human message missing from canonical ledger: envelope=%+v err=%v", envelope, err)
	}
	statuses, err := ledger.ListDeliveryStatus(t.Context(), projectID)
	if err != nil || len(statuses) != 1 || statuses[0].DeliveryID != *first.DeliveryID {
		t.Fatalf("human delivery missing from project outbox: statuses=%+v err=%v", statuses, err)
	}
	homeResponse, home := getSessionHome(t, ts, projectID, ts.adminCookie)
	assertStatus(t, homeResponse, http.StatusOK)
	if len(home.Sessions) != 1 || home.Sessions[0].Inbox.UnreadCount != 1 || home.Totals.Unread != 1 {
		t.Fatalf("human message missing from session home: %+v", home)
	}

	replayResponse, replay := postSessionUtterance(t, ts, projectID, ts.adminCookie, utteranceID, "Please inspect the build", selection)
	assertStatus(t, replayResponse, http.StatusCreated)
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay differs:\nfirst=%+v\nreplay=%+v", first, replay)
	}
	var messageCount, deliveryCount, receiptCount int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE message_id=?`, first.MessageID).Scan(&messageCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries WHERE delivery_id=?`, *first.DeliveryID).Scan(&deliveryCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM session_utterance_receipts WHERE utterance_id=?`, utteranceID).Scan(&receiptCount)
	if messageCount != 1 || deliveryCount != 1 || receiptCount != 1 {
		t.Fatalf("replay duplicated rows: messages=%d deliveries=%d receipts=%d", messageCount, deliveryCount, receiptCount)
	}

	conflict, _ := postSessionUtterance(t, ts, projectID, ts.adminCookie, utteranceID, "Different text", selection)
	assertStatus(t, conflict, http.StatusConflict)
	var problem map[string]any
	decode(t, conflict, &problem)
	if problem["code"] != "session_utterance_idempotency_conflict" {
		t.Fatalf("unexpected conflict: %#v", problem)
	}

	concurrentID := "utt_77777777777777777777777777777777"
	concurrentBody, err := json.Marshal(map[string]any{
		"schema_version": 1, "utterance_id": concurrentID, "text": "One physical gesture", "selected_session": selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	csrfToken := csrfTokenForSessionCookie(t, ts.adminCookie)
	start := make(chan struct{})
	type concurrentResult struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan concurrentResult, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			req, requestErr := http.NewRequest(http.MethodPost,
				fmt.Sprintf("%s/api/projects/%d/session-utterances/v1", ts.srv.URL, projectID), bytes.NewReader(concurrentBody))
			if requestErr != nil {
				results <- concurrentResult{err: requestErr}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", ts.srv.URL)
			req.Header.Set("Cookie", ts.adminCookie)
			req.Header.Set("X-CSRF-Token", csrfToken)
			response, requestErr := http.DefaultClient.Do(req)
			if requestErr != nil {
				results <- concurrentResult{err: requestErr}
				return
			}
			raw, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			results <- concurrentResult{status: response.StatusCode, body: raw, err: readErr}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var concurrentResponses []sessionUtteranceTestResult
	for item := range results {
		if item.err != nil || item.status != http.StatusCreated {
			t.Fatalf("concurrent replay failed: status=%d body=%s err=%v", item.status, item.body, item.err)
		}
		var decoded sessionUtteranceTestResult
		if err := json.Unmarshal(item.body, &decoded); err != nil {
			t.Fatal(err)
		}
		concurrentResponses = append(concurrentResponses, decoded)
	}
	if len(concurrentResponses) != 2 || !reflect.DeepEqual(concurrentResponses[0], concurrentResponses[1]) {
		t.Fatalf("concurrent replays diverged: %+v", concurrentResponses)
	}
	var concurrentReceipts int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM session_utterance_receipts WHERE utterance_id=?`, concurrentID).Scan(&concurrentReceipts); err != nil {
		t.Fatal(err)
	}
	if concurrentReceipts != 1 {
		t.Fatalf("concurrent gesture created %d receipts", concurrentReceipts)
	}
}

func TestSessionUtteranceNullSelectionContinuesOnePaimosConversation(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "test")
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Paimos route", "PRT")

	firstResponse, first := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_11111111111111111111111111111111", "First thought", nil)
	assertStatus(t, firstResponse, http.StatusCreated)
	secondResponse, second := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_22222222222222222222222222222222", "Second thought", nil)
	assertStatus(t, secondResponse, http.StatusCreated)
	if first.RouteKind != "paimos" || first.DeliveryID != nil || first.ProductSessionID == "" ||
		second.ProductSessionID != first.ProductSessionID || second.ThreadID != first.ProductSessionID || second.DeliveryID != nil {
		t.Fatalf("conversation did not continue: first=%+v second=%+v", first, second)
	}
	var bindings, sessions, messages, deliveries int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM paimos_conversation_bindings WHERE project_id=?`, projectID).Scan(&bindings)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM product_sessions WHERE project_id=? AND target_kind='paimos'`, projectID).Scan(&sessions)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE product_session_id=? AND role='human'`, first.ProductSessionID).Scan(&messages)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries`).Scan(&deliveries)
	if bindings != 1 || sessions != 1 || messages != 2 || deliveries != 0 {
		t.Fatalf("wrong Paimos route rows: bindings=%d sessions=%d messages=%d deliveries=%d", bindings, sessions, messages, deliveries)
	}
}

func TestSessionUtteranceWireEnvelopePreservesDecodedTextLimit(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "test")
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Escaped voice", "EVR")
	text := strings.Repeat("\\", 8*1024)

	response, result := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", text, nil)
	assertStatus(t, response, http.StatusCreated)
	if result.RouteKind != "paimos" || result.MessageID == "" {
		t.Fatalf("escaped transcript was not delivered: %+v", result)
	}
	var persisted string
	if err := db.DB.QueryRow(`SELECT body FROM agent_messages WHERE message_id=?`, result.MessageID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != text {
		t.Fatalf("escaped transcript changed: got=%d bytes want=%d", len(persisted), len(text))
	}

	tooLong, _ := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_cccccccccccccccccccccccccccccccc", text+"\\", nil)
	assertStatus(t, tooLong, http.StatusBadRequest)
	tooLong.Body.Close()
}

func TestSessionUtteranceRejectsStaleUnavailableInvalidAndMissingCSRF(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "test")
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Voice rejects", "VRJ")
	agentID := seedSessionHomeAgent(t, projectID, "worker")
	sessionID := seedSessionHomeProductSession(t, projectID, "project_agent", &agentID, nil, "Worker", "2026-08-31T10:00:00.000Z")

	stale, _ := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_33333333333333333333333333333333", "Hello", map[string]any{"product_session_id": sessionID, "revision": 2})
	assertStatus(t, stale, http.StatusConflict)
	var staleProblem map[string]any
	decode(t, stale, &staleProblem)
	if staleProblem["code"] != "session_utterance_selection_stale" {
		t.Fatalf("unexpected stale response: %#v", staleProblem)
	}

	unavailable, _ := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_44444444444444444444444444444444", "Hello", map[string]any{"product_session_id": sessionID, "revision": 1})
	assertStatus(t, unavailable, http.StatusConflict)
	var unavailableProblem map[string]any
	decode(t, unavailable, &unavailableProblem)
	if unavailableProblem["code"] != "session_utterance_target_unavailable" {
		t.Fatalf("unexpected unavailable response: %#v", unavailableProblem)
	}
	targetID, _ := seedSessionUtteranceTarget(t, projectID, agentID, "worker")
	if _, err := db.DB.Exec(`UPDATE agent_message_targets SET enabled=0 WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	revokedTarget, _ := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Must not reach a revoked target", map[string]any{"product_session_id": sessionID, "revision": 1})
	assertStatus(t, revokedTarget, http.StatusConflict)
	var revokedTargetProblem map[string]any
	decode(t, revokedTarget, &revokedTargetProblem)
	if revokedTargetProblem["code"] != "session_utterance_target_unavailable" {
		t.Fatalf("unexpected revoked-target response: %#v", revokedTargetProblem)
	}
	if _, err := db.DB.Exec(`UPDATE agent_message_targets SET enabled=1 WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT OR REPLACE INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='none' WHERE user_id=? AND project_id=?`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	revoked, _ := postSessionUtterance(t, ts, projectID, ts.memberCookie,
		"utt_99999999999999999999999999999999", "Must fail closed", map[string]any{"product_session_id": sessionID, "revision": 1})
	assertStatus(t, revoked, http.StatusNotFound)
	revoked.Body.Close()

	foreignProjectID := seedBatchProject(t, "Foreign voice", "FVR")
	foreignAgentID := seedSessionHomeAgent(t, foreignProjectID, "foreign")
	foreignSessionID := seedSessionHomeProductSession(t, foreignProjectID, "project_agent", &foreignAgentID, nil, "Foreign", "2026-08-31T10:00:00.000Z")
	foreign, _ := postSessionUtterance(t, ts, projectID, ts.adminCookie,
		"utt_88888888888888888888888888888888", "Hello", map[string]any{"product_session_id": foreignSessionID, "revision": 1})
	assertStatus(t, foreign, http.StatusNotFound)
	foreign.Body.Close()

	invalidBodies := [][]byte{
		[]byte(`{"schema_version":1,"utterance_id":"bad","text":"Hello","selected_session":null}`),
		[]byte(`{"schema_version":1,"utterance_id":"utt_55555555555555555555555555555555","text":" padded ","selected_session":null}`),
		[]byte(`{"schema_version":1,"utterance_id":"utt_55555555555555555555555555555555","text":"Hello"}`),
	}
	for _, body := range invalidBodies {
		response := postSessionUtteranceRaw(t, ts, projectID, ts.adminCookie, body, true)
		if response.StatusCode != http.StatusBadRequest {
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("invalid request status=%d body=%s", response.StatusCode, raw)
		}
		response.Body.Close()
	}
	var capturedLogs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&capturedLogs)
	audioResponse := postSessionUtteranceRaw(t, ts, projectID, ts.adminCookie,
		[]byte(`{"schema_version":1,"utterance_id":"utt_55555555555555555555555555555555","text":"Hello","selected_session":null,"audio":"secret-audio-bytes"}`), true)
	log.SetOutput(previousLogWriter)
	assertStatus(t, audioResponse, http.StatusBadRequest)
	audioResponse.Body.Close()
	if strings.Contains(capturedLogs.String(), "secret-audio-bytes") {
		t.Fatal("raw audio escaped into logs")
	}
	var persistedAudio int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages
		WHERE instr(body,'secret-audio-bytes')>0 OR instr(parts_json,'secret-audio-bytes')>0 OR instr(metadata_json,'secret-audio-bytes')>0`).Scan(&persistedAudio); err != nil {
		t.Fatal(err)
	}
	if persistedAudio != 0 {
		t.Fatalf("raw audio escaped into %d durable messages", persistedAudio)
	}

	valid, err := json.Marshal(map[string]any{
		"schema_version": 1, "utterance_id": "utt_66666666666666666666666666666666", "text": "Hello", "selected_session": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	missingCSRF := postSessionUtteranceRaw(t, ts, projectID, ts.adminCookie, valid, false)
	if missingCSRF.StatusCode == http.StatusCreated {
		t.Fatal("missing CSRF unexpectedly committed")
	}
	if missingCSRF.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("missing-CSRF cache policy=%q", missingCSRF.Header.Get("Cache-Control"))
	}
	missingCSRF.Body.Close()
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM session_utterance_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected requests committed %d receipts", count)
	}
}
