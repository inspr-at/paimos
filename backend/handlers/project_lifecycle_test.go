// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type projectLifecycleRow struct {
	ID     int64  `json:"id"`
	Key    string `json:"key"`
	Status string `json:"status"`
}

func setProjectLifecycle(t *testing.T, ts *testServer, projectID int64, status string) projectLifecycleRow {
	t.Helper()
	resp := ts.put(t, fmt.Sprintf("/api/projects/%d", projectID), ts.adminCookie, map[string]any{"status": status})
	assertStatus(t, resp, http.StatusOK)
	var row projectLifecycleRow
	decode(t, resp, &row)
	return row
}

func lifecycleError(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)
	return body.Error
}

func TestProjectLifecycleListAndValidation(t *testing.T) {
	ts := newTestServer(t)
	frozenID := createTestProject(t, ts, "Frozen Project", "FRZN")
	archivedID := createTestProject(t, ts, "Archived Project", "ARCH")
	deletedID := createTestProject(t, ts, "Deleted Project", "DELT")

	if got := setProjectLifecycle(t, ts, frozenID, " FROZEN "); got.Status != "frozen" {
		t.Fatalf("normalized status = %q, want frozen", got.Status)
	}
	setProjectLifecycle(t, ts, archivedID, "archived")
	assertStatus(t, ts.del(t, fmt.Sprintf("/api/projects/%d", deletedID), ts.adminCookie), http.StatusNoContent)

	for _, tc := range []struct {
		query     string
		wantIDs   []int64
		rejectIDs []int64
	}{
		{"", nil, []int64{frozenID, archivedID, deletedID}},
		{"?status=frozen", []int64{frozenID}, []int64{archivedID, deletedID}},
		{"?status=archived", []int64{archivedID}, []int64{frozenID, deletedID}},
		{"?status=all", []int64{frozenID, archivedID}, []int64{deletedID}},
	} {
		resp := ts.get(t, "/api/projects"+tc.query, ts.adminCookie)
		assertStatus(t, resp, http.StatusOK)
		var rows []projectLifecycleRow
		decode(t, resp, &rows)
		seen := map[int64]bool{}
		for _, row := range rows {
			seen[row.ID] = true
		}
		for _, id := range tc.wantIDs {
			if !seen[id] {
				t.Errorf("query %q omitted project %d", tc.query, id)
			}
		}
		for _, id := range tc.rejectIDs {
			if seen[id] {
				t.Errorf("query %q unexpectedly included project %d", tc.query, id)
			}
		}
	}

	badList := ts.get(t, "/api/projects?status=paused", ts.adminCookie)
	assertStatus(t, badList, http.StatusBadRequest)
	if !strings.Contains(lifecycleError(t, badList), "active, frozen, archived, deleted, or all") {
		t.Fatal("invalid list status did not name the supported lifecycle")
	}

	badUpdate := ts.put(t, fmt.Sprintf("/api/projects/%d", frozenID), ts.adminCookie, map[string]any{"status": "paused"})
	assertStatus(t, badUpdate, http.StatusBadRequest)
	if !strings.Contains(lifecycleError(t, badUpdate), "active, frozen, archived, or deleted") {
		t.Fatal("invalid update status did not name the supported lifecycle")
	}
}

func TestProjectLifecycleRejectsNewWorkButFrozenKeepsExistingWorkEditable(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "Lifecycle Work", "LFCY")
	issuesURL := fmt.Sprintf("/api/projects/%d/issues", projectID)

	created := ts.post(t, issuesURL, ts.adminCookie, map[string]any{
		"title": "Existing work", "type": "ticket", "status": "backlog",
	})
	assertStatus(t, created, http.StatusCreated)
	issueID := responseID(t, created)

	setProjectLifecycle(t, ts, projectID, "frozen")

	blocked := ts.post(t, issuesURL, ts.adminCookie, map[string]any{
		"title": "Must not be created", "type": "ticket",
	})
	assertStatus(t, blocked, http.StatusConflict)
	if got := lifecycleError(t, blocked); got != "project is frozen; new issues are disabled" {
		t.Fatalf("frozen error = %q", got)
	}

	updated := ts.put(t, fmt.Sprintf("/api/issues/%d", issueID), ts.adminCookie, map[string]any{"title": "Existing work updated"})
	assertStatus(t, updated, http.StatusOK)

	clone := ts.post(t, fmt.Sprintf("/api/issues/%d/clone", issueID), ts.adminCookie, map[string]any{})
	assertStatus(t, clone, http.StatusConflict)

	batch := ts.post(t, "/api/projects/LFCY/issues/batch", ts.adminCookie, []map[string]any{{
		"title": "Batch item", "type": "ticket",
	}})
	assertStatus(t, batch, http.StatusConflict)

	setProjectLifecycle(t, ts, projectID, "archived")
	archived := ts.post(t, issuesURL, ts.adminCookie, map[string]any{
		"title": "Archived item", "type": "ticket",
	})
	assertStatus(t, archived, http.StatusConflict)
	if got := lifecycleError(t, archived); got != "project is archived; new issues are disabled" {
		t.Fatalf("archived error = %q", got)
	}
}

func TestProjectLifecycleRejectsMoveIntoFrozenProject(t *testing.T) {
	ts := newTestServer(t)
	sourceID := createTestProject(t, ts, "Move Source", "MVSO")
	targetID := createTestProject(t, ts, "Move Target", "MVTA")
	created := ts.post(t, fmt.Sprintf("/api/projects/%d/issues", sourceID), ts.adminCookie, map[string]any{
		"title": "Stay put", "type": "ticket", "status": "backlog",
	})
	assertStatus(t, created, http.StatusCreated)
	issueID := responseID(t, created)
	setProjectLifecycle(t, ts, targetID, "frozen")

	move := ts.post(t, fmt.Sprintf("/api/issues/%d/move", issueID), ts.adminCookie, map[string]any{"project_id": targetID})
	assertStatus(t, move, http.StatusConflict)
	if got := lifecycleError(t, move); got != "project is frozen; new issues are disabled" {
		t.Fatalf("move error = %q", got)
	}
	if projectID, _ := issueProjectAndNumber(t, issueID); projectID != sourceID {
		t.Fatalf("blocked move changed project_id to %d, want %d", projectID, sourceID)
	}
}
