// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func getSessionHomeZoom(t *testing.T, ts *testServer, projectID int64, query, cookie string) (*http.Response, models.SessionHomeZoomSnapshot) {
	t.Helper()
	path := fmt.Sprintf("/api/projects/%d/session-home/zoom/v1", projectID)
	if query != "" {
		path += "?" + query
	}
	response := ts.get(t, path, cookie)
	var snapshot models.SessionHomeZoomSnapshot
	if response.StatusCode == http.StatusOK {
		decode(t, response, &snapshot)
	}
	return response, snapshot
}

func TestSessionHomeZoomV1StrictQueryEmptyAndUnboundedCanonicalZoom(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Zoom parser", "ZPR")

	response, snapshot := getSessionHomeZoom(t, ts, projectID, "", ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("Cache-Control") != "private, no-store" || snapshot.SchemaVersion != 1 ||
		snapshot.ProjectID != projectID || snapshot.Zoom != "10" || snapshot.Band != "overview" ||
		snapshot.SampleLimit != 10 || snapshot.SampleTruncated || snapshot.Sessions == nil ||
		len(snapshot.Sessions) != 0 || snapshot.SelectedSession != nil || snapshot.Totals != (models.SessionHomeZoomTotals{}) {
		t.Fatalf("empty/default contract response=%+v cache=%q", snapshot, response.Header.Get("Cache-Control"))
	}

	huge := strings.Repeat("9", 64)
	response, snapshot = getSessionHomeZoom(t, ts, projectID, "zoom="+huge, ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if snapshot.Zoom != huge || snapshot.Band != "far" || snapshot.SampleLimit != 100 {
		t.Fatalf("unbounded canonical zoom was narrowed: %+v", snapshot)
	}

	validSelection := uuid.NewString()
	invalidQueries := []string{
		"zoom=", "zoom=0", "zoom=01", "zoom=-1", "zoom=1.0", "zoom=1e2", "zoom=%201",
		"zoom=" + strings.Repeat("9", 65), "other=1", "zoom=1&zoom=2",
		"selected_session_id=" + validSelection + "&selected_session_id=" + validSelection,
		"selected_session_id=" + strings.ToUpper(validSelection), "selected_session_id=not-a-uuid", "zoom=1;x=2",
	}
	for _, query := range invalidQueries {
		t.Run(query, func(t *testing.T) {
			invalid, _ := getSessionHomeZoom(t, ts, projectID, query, ts.adminCookie)
			assertStatus(t, invalid, http.StatusBadRequest)
			if invalid.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("invalid query cache policy=%q", invalid.Header.Get("Cache-Control"))
			}
		})
	}
}

func TestSessionHomeZoomV1ExactDeduplicatedTotalsExceptionFirstAndSelectedHydration(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Zoom truth", "ZTR")
	senderID := seedSessionHomeAgent(t, projectID, "sender")
	actionID := seedSessionHomeAgent(t, projectID, "action")
	heldID := seedSessionHomeAgent(t, projectID, "held")
	normalID := seedSessionHomeAgent(t, projectID, "normal")
	seedSessionHomeHarness(t, projectID, actionID, "action", "codex", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true}, true)

	actionOlder := seedSessionHomeProductSession(t, projectID, "project_agent", &actionID, nil, "Action older", "2026-08-30T10:00:00.000Z")
	actionNewer := seedSessionHomeProductSession(t, projectID, "project_agent", &actionID, nil, "Action newer", "2026-08-30T12:00:00.000Z")
	heldSession := seedSessionHomeProductSession(t, projectID, "project_agent", &heldID, nil, "Held", "2026-08-30T11:00:00.000Z")
	seedSessionHomeProductSession(t, projectID, "project_agent", &normalID, nil, "Normal", "2026-08-30T13:00:00.000Z")
	paimosSession := seedSessionHomeProductSession(t, projectID, "paimos", nil, nil, "Paimos", "2026-08-30T14:00:00.000Z")

	seedSessionHomeMessage(t, projectID, senderID, actionID, "codex:action", true, false, "action unread")
	seedSessionHomeMessage(t, projectID, senderID, actionID, "codex:action", false, true, "action request")
	seedSessionHomeMessage(t, projectID, senderID, heldID, "codex:held", true, false, "held unread one")
	seedSessionHomeMessage(t, projectID, senderID, heldID, "codex:held", true, false, "held unread two")
	seedSessionHomeMessage(t, projectID, senderID, heldID, "codex:held", false, false, "held one")
	seedSessionHomeMessage(t, projectID, senderID, heldID, "codex:held", false, false, "held two")
	seedSessionHomeMessage(t, projectID, senderID, normalID, "codex:normal", true, false, "normal unread")

	response, snapshot := getSessionHomeZoom(t, ts, projectID, "zoom=3", ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if snapshot.Band != "detail" || snapshot.SampleLimit != 3 || !snapshot.SampleTruncated || len(snapshot.Sessions) != 3 {
		t.Fatalf("bounded sample metadata=%+v", snapshot)
	}
	wantOrder := []string{actionNewer, heldSession, actionOlder}
	for index, want := range wantOrder {
		if snapshot.Sessions[index].ProductSessionID != want {
			t.Fatalf("exception-first order[%d]=%s want=%s rows=%+v", index, snapshot.Sessions[index].ProductSessionID, want, snapshot.Sessions)
		}
	}
	wantTotals := models.SessionHomeZoomTotals{
		Sessions: 5, Unread: 4, AttentionSessions: 3, ExceptionMessages: 3,
		ActionRequests: 1, ExceptionTargets: 2, SampledExceptionTargets: 2,
	}
	if snapshot.Totals != wantTotals {
		t.Fatalf("deduplicated totals=%+v want=%+v", snapshot.Totals, wantTotals)
	}
	if snapshot.Sessions[0].Attention.ActionRequestCount != 1 || snapshot.Sessions[1].Attention.ExceptionCount != 2 ||
		snapshot.Sessions[2].Inbox.UnreadCount != 1 || snapshot.Sessions[0].Harness == nil ||
		snapshot.Sessions[0].Controls != (models.SessionHomeControls{Steer: "direct", Interrupt: true, Stop: true}) {
		t.Fatalf("shared-target composition drifted: %+v", snapshot.Sessions)
	}

	selectedQuery := "zoom=1&selected_session_id=" + url.QueryEscape(paimosSession)
	response, selected := getSessionHomeZoom(t, ts, projectID, selectedQuery, ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if len(selected.Sessions) != 1 || selected.Sessions[0].ProductSessionID != actionNewer ||
		selected.SelectedSession == nil || selected.SelectedSession.ProductSessionID != paimosSession ||
		selected.SelectedSession.Target.Kind != "paimos" {
		t.Fatalf("outside-sample selected hydration=%+v", selected)
	}

	response, selected = getSessionHomeZoom(t, ts, projectID, "zoom=1&selected_session_id="+actionNewer, ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if selected.SelectedSession == nil {
		t.Fatal("sampled selected session is null")
	}
	sampledJSON, _ := json.Marshal(selected.Sessions[0])
	selectedJSON, _ := json.Marshal(selected.SelectedSession)
	if string(sampledJSON) != string(selectedJSON) {
		t.Fatalf("sampled selection is not byte-equivalent: sample=%s selected=%s", sampledJSON, selectedJSON)
	}
}

func TestSessionHomeZoomV1SelectionConcealmentAndLargeBoundedSample(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Large zoom", "LZM")
	otherProjectID := seedBatchProject(t, "Foreign zoom", "FZM")
	foreignID := seedSessionHomeProductSession(t, otherProjectID, "paimos", nil, nil, "Foreign secret", "2026-08-30T12:00:00.000Z")

	var actorID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,'paimos',?,'',?,?,'2026-08-30T12:00:00.000Z','2026-08-30T12:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 1005)
	for index := range ids {
		ids[index] = uuid.NewString()
		if _, err := statement.Exec(ids[index], projectID, fmt.Sprintf("Session %04d", index), actorID, actorID); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	response, snapshot := getSessionHomeZoom(t, ts, projectID, "zoom=100", ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if snapshot.Totals.Sessions != 1005 || len(snapshot.Sessions) != 100 || !snapshot.SampleTruncated ||
		snapshot.SampleLimit != 100 || snapshot.Band != "aggregate" {
		t.Fatalf("large bounded sample=%+v", snapshot)
	}
	for index := 1; index < len(snapshot.Sessions); index++ {
		if snapshot.Sessions[index-1].ProductSessionID > snapshot.Sessions[index].ProductSessionID {
			t.Fatalf("UUID tiebreak is not deterministic at %d", index)
		}
	}

	for name, selectedID := range map[string]string{"missing": uuid.NewString(), "foreign": foreignID} {
		t.Run(name, func(t *testing.T) {
			denied := ts.get(t, fmt.Sprintf("/api/projects/%d/session-home/zoom/v1?zoom=10&selected_session_id=%s", projectID, selectedID), ts.adminCookie)
			assertStatus(t, denied, http.StatusNotFound)
			body, readErr := io.ReadAll(denied.Body)
			denied.Body.Close()
			if readErr != nil || string(body) != "{\"error\":\"not found\"}\n" || denied.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("selection concealment body=%q cache=%q err=%v", body, denied.Header.Get("Cache-Control"), readErr)
			}
		})
	}
}
