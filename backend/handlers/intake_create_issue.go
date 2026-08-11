// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-707 (epic PAI-703). One-click issue creation from a voice-intake
// session. Reuses the same write primitives as CreateIssue (numbering,
// enum validation, hierarchy validation, snapshot) so audit and undo
// behave identically to a manually created issue.
//
// Idempotency (INV-INTAKE-05) is layered:
//   1. IdempotencyMiddleware replays captured responses for retried POSTs.
//   2. created_issue_id on the session short-circuits any later attempt
//      with 200 + the existing issue — a completed session can never file
//      a second issue.

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// CreateIntakeIssue handles
// POST /api/projects/{id}/intake-sessions/{sessionID}/issue.
// Route guards: RequireProjectEdit (project id in path) + IdempotencyMiddleware.
func CreateIntakeIssue(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	if !requireProjectAcceptsNewIssues(w, projectID) {
		return
	}
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil || sessionID < 1 {
		jsonError(w, "intake session not found", http.StatusNotFound)
		return
	}
	s, lerr := loadIntakeSession(r.Context(), sessionID)
	if lerr != nil || (s.UserID != user.ID && !auth.IsAdmin(user)) {
		jsonError(w, "intake session not found", http.StatusNotFound)
		return
	}

	// Idempotent completion: an already-filed session answers with its issue.
	if s.CreatedIssueID != nil {
		issue := getIssueByID(*s.CreatedIssueID)
		if issue == nil {
			jsonError(w, "created issue no longer exists", http.StatusConflict)
			return
		}
		jsonOK(w, issue)
		return
	}
	if s.Status != "active" {
		jsonError(w, "intake session is not active", http.StatusConflict)
		return
	}

	// The path project must be the session's target (pinned wins over
	// detected) so a stale client can't file into the wrong project.
	target := s.activeProjectID()
	if target == nil || *target != projectID {
		jsonError(w, "project does not match the session's detected or pinned project — pin the project first", http.StatusConflict)
		return
	}

	state, serr := intakeStateAt(r.Context(), s.ID, s.Rev)
	if handleDBError(w, serr, "intake state") {
		return
	}
	var preview struct {
		Title              string `json:"title"`
		IssueType          string `json:"issue_type"`
		Description        string `json:"description"`
		AcceptanceCriteria string `json:"acceptance_criteria"`
	}
	if raw, ok := state.Artifacts["ticket_preview"]; ok {
		_ = json.Unmarshal(raw, &preview)
	}
	var spec struct {
		Markdown string `json:"markdown"`
	}
	if raw, ok := state.Artifacts["spec"]; ok {
		_ = json.Unmarshal(raw, &spec)
	}

	// Manual-first sessions (AI degraded the whole time) still create:
	// title falls back to the spec's first heading / transcript head.
	title := strings.TrimSpace(preview.Title)
	if title == "" {
		title = intakeFallbackTitle(spec.Markdown, state.Transcript)
	}
	if title == "" {
		jsonError(w, "nothing to create yet — dictate or write a specification first", http.StatusConflict)
		return
	}
	issueType := preview.IssueType
	switch issueType {
	case "ticket", "epic", "task":
	default:
		issueType = "ticket"
	}
	description := strings.TrimSpace(spec.Markdown)
	if description == "" {
		description = strings.TrimSpace(state.Transcript)
	}
	ac := preview.AcceptanceCriteria
	if issueType == "task" {
		ac = "" // FIELD_MATRIX: tasks carry no acceptance_criteria
	}

	if ev := validateEnumField("issue.type", issueType); ev != nil {
		writeEnumViolation(w, r, ev)
		return
	}
	if err := validateParent(issueType, nil, &projectID); err != nil {
		jsonError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "tx begin failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	nextNum, err := db.NextIssueNumber(r.Context(), tx, projectID)
	if err != nil {
		jsonError(w, "numbering failed", http.StatusInternalServerError)
		return
	}
	res, err := tx.ExecContext(r.Context(), `
		INSERT INTO issues(project_id, issue_number, type, title, description,
		                   acceptance_criteria, status, priority, created_by)
		VALUES(?,?,?,?,?,?, 'new', 'medium', ?)`,
		projectID, nextNum, issueType, title, description, ac, user.ID)
	if handleDBError(w, err, "issue") {
		return
	}
	issueID, _ := res.LastInsertId()

	// PAI-708: file the analysis categories as real relations
	// (touches→related, extends→follows_from, conflicts→impacts). The
	// "related" analysis bucket is decision context only — not filed.
	filedRelations := [][2]int64{} // {targetID, ...} with type index below
	filedTypes := []string{}
	if raw, ok := state.Artifacts["impacts"]; ok {
		var impacts intakeImpactsArtifact
		if json.Unmarshal(raw, &impacts) == nil && impacts.ProjectID == projectID {
			for _, e := range impacts.Impacted {
				relType := intakeCategoryRelation[e.Category]
				if relType == "" {
					continue
				}
				targetID, targetProjectID, found, err := resolveIntakeImpactTargetTx(
					r.Context(), tx, e.IssueKey,
				)
				if err != nil {
					if handleDBError(w, err, "impact relation target") {
						return
					}
					return
				}
				// The route already authorized edit access to projectID. Requiring
				// the target's current project to be identical applies that same
				// authorization to every relation without opening a cross-project
				// side channel.
				if !found || targetProjectID != projectID {
					continue
				}
				result, err := tx.ExecContext(r.Context(),
					`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type) VALUES(?,?,?)`,
					issueID, targetID, relType)
				if err != nil {
					if handleDBError(w, err, "impact relation") {
						return
					}
					return
				}
				if inserted, _ := result.RowsAffected(); inserted == 1 {
					filedRelations = append(filedRelations, [2]int64{issueID, targetID})
					filedTypes = append(filedTypes, relType)
				}
			}
		}
	}

	// Session completion inside the same tx: filing and completing are one
	// atomic step, so a crash can't leave a filed-but-active session.
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE intake_sessions
		SET created_issue_id=?, status='completed', completed_at=datetime('now'),
		    updated_at=datetime('now')
		WHERE id=?`, issueID, s.ID); err != nil {
		if handleDBError(w, err, "intake session") {
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "commit failed", http.StatusInternalServerError)
		return
	}

	issue := getIssueByID(issueID)
	if issue == nil {
		jsonError(w, "not found after insert", http.StatusInternalServerError)
		return
	}
	saveSnapshot(issue, user, r)
	for i, pair := range filedRelations {
		upsertIssueEntityRelation(pair[0], pair[1], filedTypes[i])
	}

	// Close the loop on the event log + stream.
	payload, _ := json.Marshal(map[string]any{
		"created_issue_id": issueID,
		"issue_key":        issue.IssueKey,
	})
	_ = appendIntakeStatusEvent(r, s.ID, string(payload))
	publishIntakeSessionState(r.Context(), s.ID)

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, issue)
}

// resolveIntakeImpactTargetTx re-resolves an artifact's issue key against the
// current database state inside the issue-creation transaction. Live keys win
// over move aliases, matching the public resolver, while deleted issues and
// deleted projects fail closed.
func resolveIntakeImpactTargetTx(ctx context.Context, tx *sql.Tx, ref string) (int64, int64, bool, error) {
	if !auth.IsIssueKey(ref) {
		return 0, 0, false, nil
	}
	dash := strings.LastIndex(ref, "-")
	projectKey := ref[:dash]
	issueNumber, err := strconv.Atoi(ref[dash+1:])
	if err != nil {
		return 0, 0, false, nil
	}

	var issueID, projectID int64
	err = tx.QueryRowContext(ctx, `
		SELECT i.id, i.project_id
		FROM issues i
		JOIN projects p ON p.id=i.project_id
		WHERE p.key=? AND i.issue_number=?
		  AND i.deleted_at IS NULL AND p.status!='deleted'`,
		projectKey, issueNumber,
	).Scan(&issueID, &projectID)
	if err == nil {
		return issueID, projectID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, err
	}

	err = tx.QueryRowContext(ctx, `
		SELECT i.id, i.project_id
		FROM issue_key_aliases a
		JOIN issues i ON i.id=a.issue_id
		JOIN projects p ON p.id=i.project_id
		WHERE a.project_key=? AND a.issue_number=?
		  AND i.deleted_at IS NULL AND p.status!='deleted'`,
		projectKey, issueNumber,
	).Scan(&issueID, &projectID)
	if err == nil {
		return issueID, projectID, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	return 0, 0, false, err
}

func appendIntakeStatusEvent(r *http.Request, sessionID int64, payload string) error {
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(r.Context(), tx, sessionID, "status", "system", "", payload)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	publishIntakeEvent(r.Context(), sessionID, seq)
	return nil
}

// intakeFallbackTitle derives a title when no AI preview exists: the spec's
// first heading, else the transcript's first sentence fragment.
func intakeFallbackTitle(specMarkdown, transcript string) string {
	for line := range strings.SplitSeq(specMarkdown, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return truncateIntakeTitle(line)
		}
	}
	t := strings.TrimSpace(transcript)
	if t == "" {
		return ""
	}
	if cut := strings.IndexAny(t, ".!?\n"); cut > 0 {
		t = t[:cut]
	}
	return truncateIntakeTitle(t)
}

func truncateIntakeTitle(s string) string {
	const maxLen = 120
	if len(s) <= maxLen {
		return s
	}
	return strings.TrimSpace(s[:maxLen-1]) + "…"
}
