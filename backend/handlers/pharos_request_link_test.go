package handlers_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func TestIssuePharosRequestLinkLifecycle(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "Pharos Link", "PHL")
	createPath := fmt.Sprintf("/api/projects/%d/issues", projectID)
	requestID := "pharos-create-csb1-1787912345000-1"

	created := ts.post(t, createPath, ts.adminCookie, map[string]any{
		"title":             "Need a host",
		"type":              "ticket",
		"pharos_request_id": requestID,
	})
	assertStatus(t, created, http.StatusCreated)
	var issue models.Issue
	decode(t, created, &issue)
	if issue.PharosRequestID == nil || *issue.PharosRequestID != requestID {
		t.Fatalf("created pharos_request_id=%v, want %q", issue.PharosRequestID, requestID)
	}

	t.Run("invalid free text is rejected without changing the link", func(t *testing.T) {
		resp := ts.put(t, "/api/issues/"+itoa(issue.ID), ts.adminCookie, map[string]any{
			"pharos_request_id": "https://pharos.invalid/requests/secret",
		})
		assertStatus(t, resp, http.StatusUnprocessableEntity)
		resp.Body.Close()

		got := ts.get(t, "/api/issues/"+itoa(issue.ID), ts.adminCookie)
		assertStatus(t, got, http.StatusOK)
		var unchanged models.Issue
		decode(t, got, &unchanged)
		if unchanged.PharosRequestID == nil || *unchanged.PharosRequestID != requestID {
			t.Fatalf("invalid update changed link to %v", unchanged.PharosRequestID)
		}
	})

	t.Run("empty string clears the optional link", func(t *testing.T) {
		resp := ts.put(t, "/api/issues/"+itoa(issue.ID), ts.adminCookie, map[string]any{
			"pharos_request_id": "",
		})
		assertStatus(t, resp, http.StatusOK)
		var cleared models.Issue
		decode(t, resp, &cleared)
		if cleared.PharosRequestID != nil {
			t.Fatalf("cleared pharos_request_id=%q, want null", *cleared.PharosRequestID)
		}
	})

	t.Run("clone does not duplicate a one-request link", func(t *testing.T) {
		set := ts.put(t, "/api/issues/"+itoa(issue.ID), ts.adminCookie, map[string]any{
			"pharos_request_id": requestID,
		})
		assertStatus(t, set, http.StatusOK)
		set.Body.Close()

		cloneResponse := ts.post(t, "/api/issues/"+itoa(issue.ID)+"/clone", ts.adminCookie, map[string]any{})
		assertStatus(t, cloneResponse, http.StatusCreated)
		var clone models.Issue
		decode(t, cloneResponse, &clone)
		if clone.PharosRequestID != nil {
			t.Fatalf("clone inherited pharos_request_id=%q", *clone.PharosRequestID)
		}
	})

	t.Run("undo and redo restore the link", func(t *testing.T) {
		var logID int64
		if err := db.DB.QueryRow(`SELECT id FROM mutation_log WHERE subject_type='issue' AND subject_id=? AND mutation_type='issue.update' ORDER BY id DESC LIMIT 1`, issue.ID).Scan(&logID); err != nil {
			t.Fatalf("find link mutation: %v", err)
		}
		undo := ts.post(t, fmt.Sprintf("/api/undo/%d", logID), ts.adminCookie, map[string]any{})
		assertStatus(t, undo, http.StatusOK)
		undo.Body.Close()
		var afterUndo string
		if err := db.DB.QueryRow(`SELECT pharos_request_id FROM issues WHERE id=?`, issue.ID).Scan(&afterUndo); err != nil || afterUndo != "" {
			t.Fatalf("link after undo=%q err=%v, want empty", afterUndo, err)
		}

		redo := ts.post(t, fmt.Sprintf("/api/redo/%d", logID), ts.adminCookie, map[string]any{})
		assertStatus(t, redo, http.StatusOK)
		redo.Body.Close()
		var afterRedo string
		if err := db.DB.QueryRow(`SELECT pharos_request_id FROM issues WHERE id=?`, issue.ID).Scan(&afterRedo); err != nil || afterRedo != requestID {
			t.Fatalf("link after redo=%q err=%v, want %q", afterRedo, err, requestID)
		}
	})
}

func TestIssuePharosRequestLinkRejectsInvalidCreate(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "Pharos Link Invalid", "PLI")
	resp := ts.post(t, fmt.Sprintf("/api/projects/%d/issues", projectID), ts.adminCookie, map[string]any{
		"title":             "Must not persist",
		"type":              "ticket",
		"pharos_request_id": "sk_test_abcdefghijklmnopqrstuvwxyz",
	})
	assertStatus(t, resp, http.StatusUnprocessableEntity)
	resp.Body.Close()
}
