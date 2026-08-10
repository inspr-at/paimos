// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

func appendIntakeImpactsArtifact(t *testing.T, sessionID int64, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := db.DB.QueryRow(
		`UPDATE intake_sessions SET rev=rev+1 WHERE id=? RETURNING rev`, sessionID,
	).Scan(&seq); err != nil {
		t.Fatalf("bump intake revision: %v", err)
	}
	if _, err := db.DB.Exec(`
		INSERT INTO intake_events(session_id, seq, kind, source, payload_json)
		VALUES(?, ?, 'impacts', 'ai', ?)`, sessionID, seq, string(raw)); err != nil {
		t.Fatalf("append impacts artifact: %v", err)
	}
}

// seedIntakeForCreate builds a session with a manual spec targeting a
// pinned project, ready for one-click creation.
func seedIntakeForCreate(t *testing.T, ts *testServer) (projectID int64, sessionID int64) {
	t.Helper()
	projectID = seedBatchProject(t, "Intake Target", "ITGT")
	s := createIntakeSession(t, ts, ts.memberCookie)
	sessionID = s.ID
	postChunk(t, ts, ts.memberCookie, sessionID, "We need an export button on the report page.")
	resp := ts.patch(t, "/api/intake/sessions/"+itoa(sessionID), ts.memberCookie,
		map[string]any{"spec_markdown": "# Report export button\n\n## Summary\nAdd CSV export.", "pinned_project_id": projectID})
	assertStatus(t, resp, http.StatusOK)
	return projectID, sessionID
}

// TestIntakeCreateIssue_HappyPathAndIdempotency covers INV-INTAKE-05:
// creation completes the session atomically; any replay returns the same
// issue instead of filing a second one.
func TestIntakeCreateIssue_HappyPathAndIdempotency(t *testing.T) {
	ts := newTestServer(t)
	projectID, sessionID := seedIntakeForCreate(t, ts)
	path := "/api/projects/" + itoa(projectID) + "/intake-sessions/" + itoa(sessionID) + "/issue"

	resp := ts.post(t, path, ts.memberCookie, map[string]any{})
	assertStatus(t, resp, http.StatusCreated)
	var issue struct {
		ID       int64  `json:"id"`
		IssueKey string `json:"issue_key"`
		Type     string `json:"type"`
		Title    string `json:"title"`
	}
	decode(t, resp, &issue)
	if issue.Type != "ticket" || issue.IssueKey == "" {
		t.Fatalf("issue = %+v", issue)
	}
	// No AI preview existed — title falls back to the spec's first heading.
	if issue.Title != "Report export button" {
		t.Fatalf("title = %q", issue.Title)
	}

	// Session completed + linked.
	resp = ts.get(t, "/api/intake/sessions/"+itoa(sessionID), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var head intakeHeadResp
	decode(t, resp, &head)
	if head.Session.Status != "completed" {
		t.Fatalf("session status = %s, want completed", head.Session.Status)
	}

	// Replay → 200 with the SAME issue, no second row.
	resp = ts.post(t, path, ts.memberCookie, map[string]any{})
	assertStatus(t, resp, http.StatusOK)
	var replay struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &replay)
	if replay.ID != issue.ID {
		t.Fatalf("replay filed a different issue: %d vs %d", replay.ID, issue.ID)
	}
	var count int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM issues WHERE project_id=?`, projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("issues in project = %d, want 1", count)
	}
}

// TestIntakeCreateIssue_Guards: project mismatch 409, empty session 409,
// non-owner 404.
func TestIntakeCreateIssue_Guards(t *testing.T) {
	ts := newTestServer(t)
	projectID, sessionID := seedIntakeForCreate(t, ts)
	otherProject := seedBatchProject(t, "Wrong Target", "WRNG")

	// Path project ≠ pinned project → 409.
	resp := ts.post(t, "/api/projects/"+itoa(otherProject)+"/intake-sessions/"+itoa(sessionID)+"/issue",
		ts.memberCookie, map[string]any{})
	assertStatus(t, resp, http.StatusConflict)

	// Session with no content → 409.
	empty := createIntakeSession(t, ts, ts.memberCookie)
	resp = ts.patch(t, "/api/intake/sessions/"+itoa(empty.ID), ts.memberCookie,
		map[string]any{"pinned_project_id": projectID})
	assertStatus(t, resp, http.StatusOK)
	resp = ts.post(t, "/api/projects/"+itoa(projectID)+"/intake-sessions/"+itoa(empty.ID)+"/issue",
		ts.memberCookie, map[string]any{})
	assertStatus(t, resp, http.StatusConflict)

	// Non-owner (admin passes, so use a fresh member) → 404.
	resp = ts.post(t, "/api/users", ts.adminCookie, map[string]string{
		"username": "intake-creator-other", "password": "otherpass123", "role": "member",
	})
	assertStatus(t, resp, http.StatusCreated)
	if _, err := db.DB.Exec(`UPDATE users SET must_change_password=0 WHERE username='intake-creator-other'`); err != nil {
		t.Fatal(err)
	}
	otherCookie := ts.login(t, "intake-creator-other", "otherpass123")
	resp = ts.post(t, "/api/projects/"+itoa(projectID)+"/intake-sessions/"+itoa(sessionID)+"/issue",
		otherCookie, map[string]any{})
	assertStatus(t, resp, http.StatusNotFound)
}

func TestIntakeCreateIssue_ImpactRelationsStayInAnalyzedProject(t *testing.T) {
	t.Run("files a valid same-project relation", func(t *testing.T) {
		ts := newTestServer(t)
		projectID, sessionID := seedIntakeForCreate(t, ts)
		targetID := seedListV2Issue(t, projectID, 50, "Conflicting export path", "backlog")
		appendIntakeImpactsArtifact(t, sessionID, map[string]any{
			"project_id": projectID,
			"impacted": []map[string]any{{
				"issue_key": "ITGT-50", "category": "conflicts",
			}},
		})

		resp := ts.post(t, "/api/projects/"+itoa(projectID)+"/intake-sessions/"+itoa(sessionID)+"/issue",
			ts.memberCookie, map[string]any{})
		assertStatus(t, resp, http.StatusCreated)
		var created struct {
			ID int64 `json:"id"`
		}
		decode(t, resp, &created)

		var count int
		if err := db.DB.QueryRow(`
			SELECT COUNT(*) FROM issue_relations
			WHERE source_id=? AND target_id=? AND type='impacts'`, created.ID, targetID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("same-project impacts relations = %d, want 1", count)
		}
		if err := db.DB.QueryRow(`
			SELECT COUNT(*) FROM entity_relations
			WHERE project_id=? AND source_type='issue' AND source_id=?
			  AND target_type='issue' AND target_id=? AND edge_type='impacts'`,
			projectID, created.ID, targetID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("same-project entity relations = %d, want 1", count)
		}
	})

	t.Run("does not file an artifact after the session is repinned", func(t *testing.T) {
		ts := newTestServer(t)
		analyzedProjectID, sessionID := seedIntakeForCreate(t, ts)
		targetID := seedListV2Issue(t, analyzedProjectID, 50, "Stale analyzed issue", "backlog")
		appendIntakeImpactsArtifact(t, sessionID, map[string]any{
			"project_id": analyzedProjectID,
			"impacted": []map[string]any{{
				"issue_key": "ITGT-50", "category": "conflicts",
			}},
		})

		createProjectID := seedBatchProject(t, "Repinned Target", "RPIN")
		resp := ts.patch(t, "/api/intake/sessions/"+itoa(sessionID), ts.memberCookie,
			map[string]any{"pinned_project_id": createProjectID})
		assertStatus(t, resp, http.StatusOK)
		resp = ts.post(t, "/api/projects/"+itoa(createProjectID)+"/intake-sessions/"+itoa(sessionID)+"/issue",
			ts.memberCookie, map[string]any{})
		assertStatus(t, resp, http.StatusCreated)
		var created struct {
			ID int64 `json:"id"`
		}
		decode(t, resp, &created)

		var count int
		if err := db.DB.QueryRow(`
			SELECT COUNT(*) FROM issue_relations WHERE source_id=? AND target_id=?`, created.ID, targetID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("stale impacts relations = %d, want 0", count)
		}
	})

	t.Run("does not file a cross-project target from a matching artifact", func(t *testing.T) {
		ts := newTestServer(t)
		projectID, sessionID := seedIntakeForCreate(t, ts)
		otherProjectID := seedBatchProject(t, "Unrelated Project", "OTHR")
		targetID := seedListV2Issue(t, otherProjectID, 1, "Unrelated private issue", "backlog")
		appendIntakeImpactsArtifact(t, sessionID, map[string]any{
			"project_id": projectID,
			"impacted": []map[string]any{{
				"issue_key": "OTHR-1", "category": "conflicts",
			}},
		})

		resp := ts.post(t, "/api/projects/"+itoa(projectID)+"/intake-sessions/"+itoa(sessionID)+"/issue",
			ts.memberCookie, map[string]any{})
		assertStatus(t, resp, http.StatusCreated)
		var created struct {
			ID int64 `json:"id"`
		}
		decode(t, resp, &created)

		var count int
		if err := db.DB.QueryRow(`
			SELECT COUNT(*) FROM issue_relations WHERE source_id=? AND target_id=?`, created.ID, targetID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cross-project impacts relations = %d, want 0", count)
		}
	})

	t.Run("does not file a deleted target", func(t *testing.T) {
		ts := newTestServer(t)
		projectID, sessionID := seedIntakeForCreate(t, ts)
		targetID := seedListV2Issue(t, projectID, 50, "Deleted analyzed issue", "backlog")
		if _, err := db.DB.Exec(`UPDATE issues SET deleted_at=datetime('now') WHERE id=?`, targetID); err != nil {
			t.Fatal(err)
		}
		appendIntakeImpactsArtifact(t, sessionID, map[string]any{
			"project_id": projectID,
			"impacted": []map[string]any{{
				"issue_key": "ITGT-50", "category": "conflicts",
			}},
		})

		resp := ts.post(t, "/api/projects/"+itoa(projectID)+"/intake-sessions/"+itoa(sessionID)+"/issue",
			ts.memberCookie, map[string]any{})
		assertStatus(t, resp, http.StatusCreated)
		var created struct {
			ID int64 `json:"id"`
		}
		decode(t, resp, &created)

		var count int
		if err := db.DB.QueryRow(`
			SELECT COUNT(*) FROM issue_relations WHERE source_id=? AND target_id=?`, created.ID, targetID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("deleted-target impacts relations = %d, want 0", count)
		}
	})
}
