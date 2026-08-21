package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/db"
)

func csrfTokenForSessionCookie(t *testing.T, cookie string) string {
	t.Helper()
	sessionID := strings.TrimPrefix(cookie, "session=")
	if sessionID == cookie || sessionID == "" {
		t.Fatalf("invalid session cookie %q", cookie)
	}
	var token string
	if err := db.DB.QueryRow(`SELECT csrf_token FROM sessions WHERE id=?`, sessionID).Scan(&token); err != nil {
		t.Fatalf("csrf token: %v", err)
	}
	return token
}

func postAgentVoice(t *testing.T, ts *testServer, path, cookie, contentType string, body []byte, csrfToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Cookie-authenticated mutations always need a same-origin signal. Tests
	// that omit/mangle only the token still exercise the intended CSRF branch.
	req.Header.Set("Origin", ts.srv.URL)
	if csrfToken != "" {
		req.Header.Set("X-CSRF-Token", csrfToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func postAgentVoiceJSON(t *testing.T, ts *testServer, cookie string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return postAgentVoice(t, ts, "/api/agent-mode/voice/speak", cookie, "application/json", raw,
		csrfTokenForSessionCookie(t, cookie))
}

func seedAgentVoiceFixture(t *testing.T, ts *testServer, projectKey, title string) (int64, int64, agentmode.DeliveryRow) {
	t.Helper()
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES(?,?,'active')`, "Voice "+projectKey, projectKey)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')`, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	issue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket',?,'in-progress')`, projectID, title)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name,
		delivery_instrumentation_version) VALUES(?,?,?,'running','voice-private-agent',0)`, issueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	response := ts.get(t, "/api/agent-mode/deliveries", ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	var snapshot agentmode.Snapshot
	decode(t, response, &snapshot)
	for _, row := range snapshot.Rows {
		if row.IssueID == issueID {
			return projectID, issueID, row
		}
	}
	t.Fatalf("voice delivery missing from snapshot: %+v", snapshot.Rows)
	return 0, 0, agentmode.DeliveryRow{}
}

func TestAgentModeVoiceSTTIsEphemeralClosedAndMetadataOnly(t *testing.T) {
	ts := newTestServer(t)
	transcript := "Private dictated note that must remain ephemeral."
	upstream := fakeScribe(t, transcript, "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	response := postAgentVoice(t, ts, "/api/agent-mode/voice/transcribe?language=en", ts.memberCookie,
		"audio/webm; codecs=opus", bytes.Repeat([]byte("a"), 8001), csrfTokenForSessionCookie(t, ts.memberCookie))
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("private response headers=%v", response.Header)
	}
	var wire map[string]json.RawMessage
	decode(t, response, &wire)
	if len(wire) != 3 || wire["utterance_id"] == nil || wire["text"] == nil || wire["final"] == nil {
		t.Fatalf("closed transcript response=%v", wire)
	}
	var utteranceID, text string
	var final bool
	_ = json.Unmarshal(wire["utterance_id"], &utteranceID)
	_ = json.Unmarshal(wire["text"], &text)
	_ = json.Unmarshal(wire["final"], &final)
	if !strings.HasPrefix(utteranceID, "utt_") || len(utteranceID) != 36 || text != transcript || !final {
		t.Fatalf("transcript id/text/final=%q/%q/%t", utteranceID, text, final)
	}

	var action, subAction, surface, outcome string
	var units int
	if err := db.DB.QueryRow(`SELECT action_key,sub_action,surface,outcome,prompt_tokens FROM ai_calls
		WHERE action_key='agent_mode_stt'`).Scan(&action, &subAction, &surface, &outcome, &units); err != nil {
		t.Fatal(err)
	}
	if action != "agent_mode_stt" || subAction != "en" || surface != "agent_mode" || outcome != "ok" || units != 3 {
		t.Fatalf("audit=%q/%q/%q/%q/%d", action, subAction, surface, outcome, units)
	}
	var intakeRows, comments, cacheRows, metadataLeaks int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM intake_events`).Scan(&intakeRows)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&comments)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM idempotency_keys`).Scan(&cacheRows)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE instr(action_key,?)>0 OR instr(sub_action,?)>0 OR
		instr(surface,?)>0 OR instr(provider,?)>0 OR instr(model,?)>0`, transcript, transcript, transcript, transcript, transcript).Scan(&metadataLeaks)
	if intakeRows != 0 || comments != 0 || cacheRows != 0 || metadataLeaks != 0 {
		t.Fatalf("persisted intake/comments/cache/leak=%d/%d/%d/%d", intakeRows, comments, cacheRows, metadataLeaks)
	}
}

// PAI-808: the wire contract bounds transcript text at 8192 UTF-8 *bytes*, and
// the handler is the authoritative producer of that bound. An upstream provider
// can return arbitrarily long ASCII or multibyte text, so this pins both halves:
// nothing above 8192 bytes escapes, and truncation never splits a code point.
func TestAgentModeVoiceSTTTextStaysValidUTF8Within8192Bytes(t *testing.T) {
	const maxBytes = 8192
	tests := []struct {
		name     string
		upstream string
		want     string
	}{
		// 8192 is not a multiple of 3, so the euro sign forces a mid-code-point
		// cut that must back off to 8190 bytes rather than emit a broken rune.
		{"oversized multibyte", strings.Repeat("€", 4000), strings.Repeat("€", maxBytes/3)},
		{"oversized ascii", strings.Repeat("a", maxBytes+1000), strings.Repeat("a", maxBytes)},
		{"at bound preserved", strings.Repeat("b", maxBytes), strings.Repeat("b", maxBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newTestServer(t)
			upstream := fakeScribe(t, test.upstream, "test-elevenlabs-key")
			defer upstream.Close()
			configureVoice(t, ts, upstream.URL)

			response := postAgentVoice(t, ts, "/api/agent-mode/voice/transcribe?language=en", ts.memberCookie,
				"audio/webm", []byte("audio"), csrfTokenForSessionCookie(t, ts.memberCookie))
			assertStatus(t, response, http.StatusOK)
			var transcript struct {
				Text string `json:"text"`
			}
			decode(t, response, &transcript)
			if len(transcript.Text) > maxBytes || !utf8.ValidString(transcript.Text) ||
				!strings.HasPrefix(test.upstream, transcript.Text) || transcript.Text != test.want {
				t.Fatalf("text bytes=%d valid=%t prefix=%t want bytes=%d", len(transcript.Text),
					utf8.ValidString(transcript.Text), strings.HasPrefix(test.upstream, transcript.Text), len(test.want))
			}
		})
	}
}

func TestAgentModeVoiceSTTNoSpeechAndUpstreamFailuresAreClosedAndAudited(t *testing.T) {
	tests := []struct {
		name       string
		serve      http.HandlerFunc
		wantStatus int
		outcome    string
		errorClass string
		units      int
	}{
		{
			name: "no speech", wantStatus: http.StatusUnprocessableEntity, outcome: "no_op", units: 1,
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"text":""}`))
			},
		},
		{
			name: "upstream status", wantStatus: http.StatusBadGateway, outcome: "fail_upstream", errorClass: "upstream",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		},
		{
			name: "malformed upstream json", wantStatus: http.StatusBadGateway, outcome: "fail_upstream", errorClass: "upstream",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"text":`))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newTestServer(t)
			upstream := httptest.NewServer(test.serve)
			defer upstream.Close()
			configureVoice(t, ts, upstream.URL)

			response := postAgentVoice(t, ts, "/api/agent-mode/voice/transcribe?language=en", ts.memberCookie,
				"audio/webm", []byte("audio"), csrfTokenForSessionCookie(t, ts.memberCookie))
			assertStatus(t, response, test.wantStatus)
			if response.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("cache-control=%q", response.Header.Get("Cache-Control"))
			}
			_ = response.Body.Close()

			var outcome, errorClass string
			var units, rows int
			if err := db.DB.QueryRow(`SELECT outcome,error_class,prompt_tokens FROM ai_calls
				WHERE action_key='agent_mode_stt'`).Scan(&outcome, &errorClass, &units); err != nil {
				t.Fatal(err)
			}
			_ = db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE action_key='agent_mode_stt'`).Scan(&rows)
			if rows != 1 || outcome != test.outcome || errorClass != test.errorClass || units != test.units {
				t.Fatalf("audit rows/outcome/class/units=%d/%q/%q/%d", rows, outcome, errorClass, units)
			}
		})
	}
}

func TestAgentModeVoiceAuthCSRFAndNoStoreOrdering(t *testing.T) {
	ts := newTestServer(t)
	path := "/api/agent-mode/voice/transcribe?language=en"
	body := []byte("audio")
	externalToken := csrfTokenForSessionCookie(t, ts.externalCookie)
	var canonical map[string]any
	for name, token := range map[string]string{"missing": "", "bad": "bad-token", "valid": externalToken} {
		t.Run("external "+name, func(t *testing.T) {
			response := postAgentVoice(t, ts, path, ts.externalCookie, "audio/webm", body, token)
			assertStatus(t, response, http.StatusNotFound)
			if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("headers=%v", response.Header)
			}
			var problem map[string]any
			decode(t, response, &problem)
			delete(problem, "request_id")
			delete(problem, "instance")
			if canonical == nil {
				canonical = problem
			} else if fmt.Sprint(problem) != fmt.Sprint(canonical) {
				t.Fatalf("canonical external response changed: got=%v want=%v", problem, canonical)
			}
		})
	}
	wrongMethodGet := ts.get(t, "/api/agent-mode/voice/transcribe", ts.externalCookie)
	wrongMethodPost := postAgentVoice(t, ts, "/api/agent-mode/deliveries", ts.externalCookie,
		"application/json", []byte(`{}`), externalToken)
	unknownPath := ts.get(t, "/api/agent-mode/not-a-route", ts.externalCookie)
	for name, response := range map[string]*http.Response{
		"wrong-method-get": wrongMethodGet, "wrong-method-post": wrongMethodPost, "unknown-path": unknownPath,
	} {
		t.Run("external "+name, func(t *testing.T) {
			assertStatus(t, response, http.StatusNotFound)
			if response.Header.Get("Cache-Control") != "private, no-store" ||
				response.Header.Get("Content-Type") != "application/problem+json" || response.Header.Get("Allow") != "" {
				t.Fatalf("concealment headers=%v", response.Header)
			}
			var problem map[string]any
			decode(t, response, &problem)
			delete(problem, "request_id")
			delete(problem, "instance")
			if fmt.Sprint(problem) != fmt.Sprint(canonical) {
				t.Fatalf("wrong-method/unknown response differs from canonical 404: got=%v want=%v", problem, canonical)
			}
		})
	}

	badCSRF := postAgentVoice(t, ts, path, ts.memberCookie, "audio/webm", body, "bad-token")
	assertStatus(t, badCSRF, http.StatusForbidden)
	if badCSRF.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("internal CSRF failure cache-control=%q", badCSRF.Header.Get("Cache-Control"))
	}
	_ = badCSRF.Body.Close()
	unauthPOST := postAgentVoice(t, ts, path, "", "audio/webm", body, "")
	assertStatus(t, unauthPOST, http.StatusUnauthorized)
	if unauthPOST.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unauth POST cache-control=%q", unauthPOST.Header.Get("Cache-Control"))
	}
	_ = unauthPOST.Body.Close()
	unauthGET := ts.get(t, "/api/agent-mode/deliveries", "")
	assertStatus(t, unauthGET, http.StatusUnauthorized)
	if unauthGET.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unauth GET cache-control=%q", unauthGET.Header.Get("Cache-Control"))
	}
	_ = unauthGET.Body.Close()
}

func TestAgentModeVoiceSTTRejectsNonClosedInputsBeforeProvider(t *testing.T) {
	ts := newTestServer(t)
	token := csrfTokenForSessionCookie(t, ts.memberCookie)
	for name, invalid := range map[string]struct {
		path      string
		mediaType string
		body      []byte
		want      int
	}{
		"missing language":   {"/api/agent-mode/voice/transcribe", "audio/webm", []byte("x"), http.StatusBadRequest},
		"duplicate language": {"/api/agent-mode/voice/transcribe?language=en&language=de", "audio/webm", []byte("x"), http.StatusBadRequest},
		"extra query":        {"/api/agent-mode/voice/transcribe?language=en&extra=1", "audio/webm", []byte("x"), http.StatusBadRequest},
		"unknown language":   {"/api/agent-mode/voice/transcribe?language=fr", "audio/webm", []byte("x"), http.StatusBadRequest},
		"unsupported mime":   {"/api/agent-mode/voice/transcribe?language=en", "application/octet-stream", []byte("x"), http.StatusUnsupportedMediaType},
		"empty audio":        {"/api/agent-mode/voice/transcribe?language=en", "audio/webm", nil, http.StatusUnprocessableEntity},
		"oversized audio":    {"/api/agent-mode/voice/transcribe?language=en", "audio/webm", make([]byte, (12<<20)+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			response := postAgentVoice(t, ts, invalid.path, ts.memberCookie, invalid.mediaType, invalid.body, token)
			assertStatus(t, response, invalid.want)
			if response.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("cache-control=%q", response.Header.Get("Cache-Control"))
			}
			_ = response.Body.Close()
		})
	}
	var calls int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE action_key='agent_mode_stt'`).Scan(&calls)
	if calls != 0 {
		t.Fatalf("rejected STT inputs emitted %d provider audit rows", calls)
	}
}

func TestAgentModeVoiceTTSUsesReauthorizedServerTemplateOnly(t *testing.T) {
	ts := newTestServer(t)
	projectID, issueID, row := seedAgentVoiceFixture(t, ts, "VTS", "TOP SECRET title must never be narrated")
	_, _, candidate := seedAgentVoiceFixture(t, ts, "VTC", "SECOND SECRET title must never be narrated")
	audio := fakeMPEGAudio()
	var mu sync.Mutex
	var spoken string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		spoken = body.Text
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	}))
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	response := postAgentVoiceJSON(t, ts, ts.memberCookie, map[string]any{
		"template": "status", "delivery_id": row.DeliveryID, "delivery_revision": row.DeliveryRevision,
		"candidate_ids": []string{}, "locale": "en",
	})
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("Content-Type") != "audio/mpeg" ||
		response.Header.Get("Content-Language") != "en" {
		t.Fatalf("TTS headers=%v", response.Header)
	}
	gotAudio, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !bytes.Equal(gotAudio, audio) {
		t.Fatalf("audio=%q", gotAudio)
	}
	mu.Lock()
	narration := spoken
	mu.Unlock()
	if !strings.Contains(narration, row.IssueKey) || strings.Contains(narration, "TOP SECRET") {
		t.Fatalf("unsafe narration=%q", narration)
	}

	var action, subAction, surface, outcome string
	var auditIssue, auditProject int64
	if err := db.DB.QueryRow(`SELECT action_key,sub_action,surface,outcome,issue_id,project_id FROM ai_calls
		WHERE action_key='agent_mode_tts'`).Scan(&action, &subAction, &surface, &outcome, &auditIssue, &auditProject); err != nil {
		t.Fatal(err)
	}
	if action != "agent_mode_tts" || subAction != "status" || surface != "agent_mode" || outcome != "ok" ||
		auditIssue != issueID || auditProject != projectID {
		t.Fatalf("TTS audit=%q/%q/%q/%q/%d/%d", action, subAction, surface, outcome, auditIssue, auditProject)
	}
	var leak int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE instr(action_key,'TOP SECRET')>0 OR
		instr(sub_action,'TOP SECRET')>0 OR instr(surface,'TOP SECRET')>0 OR instr(provider,'TOP SECRET')>0 OR instr(model,'TOP SECRET')>0`).Scan(&leak)
	if leak != 0 {
		t.Fatal("server template content leaked into ai_calls metadata")
	}

	clarification := postAgentVoiceJSON(t, ts, ts.memberCookie, map[string]any{
		"template": "clarification", "delivery_id": row.DeliveryID, "delivery_revision": row.DeliveryRevision,
		"candidate_ids": []string{candidate.DeliveryID}, "locale": "en",
	})
	assertStatus(t, clarification, http.StatusOK)
	_ = clarification.Body.Close()
	mu.Lock()
	clarificationNarration := spoken
	mu.Unlock()
	if !strings.Contains(clarificationNarration, candidate.IssueKey) ||
		strings.Contains(clarificationNarration, "TOP SECRET") || strings.Contains(clarificationNarration, "SECOND SECRET") {
		t.Fatalf("unsafe clarification narration=%q", clarificationNarration)
	}
}

func TestAgentModeVoiceTTSRejectsHostileProviderBody(t *testing.T) {
	ts := newTestServer(t)
	_, _, row := seedAgentVoiceFixture(t, ts, "VXSS", "Provider payload boundary")
	hostile := []byte(`<html><script>globalThis.pwned=true</script></html>`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A hostile or compromised provider may lie about its media type. The
		// server must validate the MPEG structure rather than reflect this body.
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(hostile)
	}))
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	response := postAgentVoiceJSON(t, ts, ts.memberCookie, map[string]any{
		"template": "status", "delivery_id": row.DeliveryID, "delivery_revision": row.DeliveryRevision,
		"candidate_ids": []string{}, "locale": "en",
	})
	assertStatus(t, response, http.StatusBadGateway)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Type") != "application/problem+json" ||
		response.Header.Get("X-Content-Type-Options") != "nosniff" || bytes.Contains(body, hostile) ||
		strings.Contains(string(body), "script") {
		t.Fatalf("unsafe provider response headers=%v body=%q", response.Header, body)
	}
	var outcome, errorClass string
	if err := db.DB.QueryRow(`SELECT outcome,error_class FROM ai_calls WHERE action_key='agent_mode_tts'`).Scan(&outcome, &errorClass); err != nil {
		t.Fatal(err)
	}
	if outcome != "fail_upstream" || errorClass != "upstream" {
		t.Fatalf("audit outcome/class=%q/%q", outcome, errorClass)
	}
}

func TestAgentModeVoiceTTSStrictRequestAndAuthorizationPrecedeConfiguration(t *testing.T) {
	ts := newTestServer(t)
	projectID, _, row := seedAgentVoiceFixture(t, ts, "VAU", "Voice auth target")
	valid := map[string]any{"template": "status", "delivery_id": row.DeliveryID,
		"delivery_revision": row.DeliveryRevision, "candidate_ids": []string{}, "locale": "en"}
	for name, mutate := range map[string]func(map[string]any){
		"arbitrary text":     func(body map[string]any) { body["text"] = "speak caller secret" },
		"missing candidates": func(body map[string]any) { delete(body, "candidate_ids") },
		"null candidates":    func(body map[string]any) { body["candidate_ids"] = nil },
		"status candidates":  func(body map[string]any) { body["candidate_ids"] = []string{row.DeliveryID} },
		"candidate cap": func(body map[string]any) {
			body["template"] = "clarification"
			body["candidate_ids"] = []string{"issue:1", "issue:2", "issue:3", "issue:4"}
		},
		"clarification empty": func(body map[string]any) {
			body["template"] = "clarification"
		},
		"unknown template": func(body map[string]any) { body["template"] = "saved" },
		"missing revision": func(body map[string]any) { delete(body, "delivery_revision") },
	} {
		t.Run(name, func(t *testing.T) {
			body := make(map[string]any, len(valid)+1)
			for key, value := range valid {
				body[key] = value
			}
			mutate(body)
			response := postAgentVoiceJSON(t, ts, ts.memberCookie, body)
			assertStatus(t, response, http.StatusBadRequest)
			_ = response.Body.Close()
		})
	}

	stale := make(map[string]any, len(valid))
	for key, value := range valid {
		stale[key] = value
	}
	stale["delivery_revision"] = "stale-revision"
	staleResponse := postAgentVoiceJSON(t, ts, ts.memberCookie, stale)
	assertStatus(t, staleResponse, http.StatusConflict)
	_ = staleResponse.Body.Close()

	var memberID int64
	_ = db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID)
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='viewer' WHERE project_id=? AND user_id=?`, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	note := make(map[string]any, len(valid))
	for key, value := range valid {
		note[key] = value
	}
	note["template"] = "note_ready"
	noteResponse := postAgentVoiceJSON(t, ts, ts.memberCookie, note)
	assertStatus(t, noteResponse, http.StatusNotFound)
	if noteResponse.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("note capability 404 cache-control=%q", noteResponse.Header.Get("Cache-Control"))
	}
	_ = noteResponse.Body.Close()

	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='editor' WHERE project_id=? AND user_id=?`, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	candidateIssue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,2,'ticket','candidate becomes terminal','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	candidateIssueID, _ := candidateIssue.LastInsertId()
	candidateRun, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name,
		delivery_instrumentation_version) VALUES(?,?,?,'running','candidate-agent',0)`, candidateIssueID, projectID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	candidateRunID, _ := candidateRun.LastInsertId()
	snapshotResponse := ts.get(t, "/api/agent-mode/deliveries", ts.memberCookie)
	assertStatus(t, snapshotResponse, http.StatusOK)
	var snapshot agentmode.Snapshot
	decode(t, snapshotResponse, &snapshot)
	var candidateID string
	for _, candidate := range snapshot.Rows {
		if candidate.IssueID == candidateIssueID {
			candidateID = candidate.DeliveryID
		}
	}
	if candidateID == "" {
		t.Fatal("active clarification candidate missing")
	}
	if _, err := db.DB.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, candidateRunID); err != nil {
		t.Fatal(err)
	}
	terminalCandidate := map[string]any{"template": "clarification", "delivery_id": row.DeliveryID,
		"delivery_revision": row.DeliveryRevision, "candidate_ids": []string{candidateID}, "locale": "en"}
	terminalResponse := postAgentVoiceJSON(t, ts, ts.memberCookie, terminalCandidate)
	assertStatus(t, terminalResponse, http.StatusNotFound)
	_ = terminalResponse.Body.Close()

	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='none' WHERE project_id=? AND user_id=?`, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	hiddenResponse := postAgentVoiceJSON(t, ts, ts.memberCookie, valid)
	assertStatus(t, hiddenResponse, http.StatusNotFound)
	_ = hiddenResponse.Body.Close()
	var providerAudits int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE action_key='agent_mode_tts'`).Scan(&providerAudits)
	if providerAudits != 0 {
		t.Fatalf("pre-provider rejection emitted %d TTS audits", providerAudits)
	}
}

func TestAgentModeVoiceAuditSurvivesCancelledRequest(t *testing.T) {
	ts := newTestServer(t)
	entered := make(chan struct{}, 1)
	providerRelease := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-providerRelease
	}))
	defer upstream.Close()
	defer close(providerRelease)
	configureVoice(t, ts, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ts.srv.URL+"/api/agent-mode/voice/transcribe?language=en", bytes.NewReader([]byte("audio")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", ts.memberCookie)
	req.Header.Set("Origin", ts.srv.URL)
	req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, ts.memberCookie))
	req.Header.Set("Content-Type", "audio/webm")
	done := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- requestErr
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider was not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled HTTP request did not return")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE action_key='agent_mode_stt'
			AND outcome='fail_upstream' AND error_class='upstream' AND prompt_tokens=0`).Scan(&count)
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled paid attempt audit count=%d", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVoiceDailyBudgetReservationIsConcurrentAndCrossSurface(t *testing.T) {
	ts := newTestServer(t)
	t.Setenv("PAIMOS_VOICE_STT_DAILY_SECONDS", "3")
	session := createIntakeSession(t, ts, ts.memberCookie)
	entered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	providerCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		providerCalls++
		call := providerCalls
		mu.Unlock()
		if call == 1 {
			entered <- struct{}{}
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "shared budget"})
	}))
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	agentDone := make(chan *http.Response, 1)
	agentErr := make(chan error, 1)
	agentCSRF := csrfTokenForSessionCookie(t, ts.memberCookie)
	go func() {
		req, err := http.NewRequest(http.MethodPost, ts.srv.URL+"/api/agent-mode/voice/transcribe?language=en",
			bytes.NewReader(bytes.Repeat([]byte("a"), 8000)))
		if err == nil {
			req.Header.Set("Cookie", ts.memberCookie)
			req.Header.Set("Origin", ts.srv.URL)
			req.Header.Set("X-CSRF-Token", agentCSRF)
			req.Header.Set("Content-Type", "audio/webm")
			var response *http.Response
			response, err = http.DefaultClient.Do(req)
			if err == nil {
				agentDone <- response
				return
			}
		}
		agentErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first cross-surface provider call did not enter")
	}

	intakeResponse := postAudio(t, ts, session.ID, "audio/webm", bytes.Repeat([]byte("b"), 8000))
	assertStatus(t, intakeResponse, http.StatusTooManyRequests)
	if intakeResponse.Header.Get("Retry-After") == "" {
		t.Fatal("shared daily budget rejection omitted Retry-After")
	}
	_ = intakeResponse.Body.Close()
	close(releaseFirst)
	select {
	case err := <-agentErr:
		t.Fatalf("agent request: %v", err)
	case response := <-agentDone:
		assertStatus(t, response, http.StatusOK)
		_ = response.Body.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("first provider request did not complete")
	}

	mu.Lock()
	calls := providerCalls
	mu.Unlock()
	var audits, units int
	_ = db.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(prompt_tokens),0) FROM ai_calls
		WHERE action_key IN ('intake_stt','agent_mode_stt')`).Scan(&audits, &units)
	if calls != 1 || audits != 1 || units != 2 {
		t.Fatalf("provider/audits/units=%d/%d/%d want 1/1/2", calls, audits, units)
	}
}
