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

package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

type Comment struct {
	ID         int64   `json:"id"`
	IssueID    int64   `json:"issue_id"`
	AuthorID   *int64  `json:"author_id"`
	Author     *string `json:"author"`
	AvatarPath *string `json:"avatar_path"`
	Body       string  `json:"body"`
	Visibility string  `json:"visibility"`
	// ClientRequestID is the optional PAI-808 exact-once identity. It is
	// intentionally absent from ordinary comments and is accepted only for
	// internal comments.
	ClientRequestID *string `json:"client_request_id,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

// Visibility levels accepted by the comments API (PAI-475). 'internal' is the
// safe default; 'external' explicitly opts-in to surfacing the comment on the
// Customer Portal sidebar (PAI-474).
const (
	CommentVisibilityInternal = "internal"
	CommentVisibilityExternal = "external"
)

func isValidCommentVisibility(v string) bool {
	return v == CommentVisibilityInternal || v == CommentVisibilityExternal
}

// GET /api/issues/{id}/comments
func ListComments(w http.ResponseWriter, r *http.Request) {
	issueID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid issue id", http.StatusBadRequest)
		return
	}
	rows, err := db.DB.Query(`
		SELECT c.id, c.issue_id, c.author_id, COALESCE(NULLIF(u.nickname,''), u.username), u.avatar_path,
		       c.body, c.visibility, c.client_request_id, c.created_at
		FROM comments c
		LEFT JOIN users u ON u.id = c.author_id
		WHERE c.issue_id = ?
		ORDER BY c.created_at ASC
	`, issueID)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	comments := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Author, &c.AvatarPath, &c.Body, &c.Visibility,
			&c.ClientRequestID, &c.CreatedAt); err == nil {
			comments = append(comments, c)
		}
	}
	jsonOK(w, comments)
}

// POST /api/issues/{id}/comments  { "body": "..." }
func CreateComment(w http.ResponseWriter, r *http.Request) {
	issueID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid issue id", http.StatusBadRequest)
		return
	}
	var body struct {
		Body            string  `json:"body"`
		Visibility      string  `json:"visibility"`
		ClientRequestID *string `json:"client_request_id"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&body); err != nil || body.Body == "" {
		jsonError(w, "body required", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	// PAI-475: default to internal — explicit opt-in is required for the
	// comment to ever land on the Customer Portal. Unknown values get
	// rejected rather than silently coerced.
	if body.Visibility == "" {
		body.Visibility = CommentVisibilityInternal
	}
	if !isValidCommentVisibility(body.Visibility) {
		jsonError(w, "invalid visibility", http.StatusBadRequest)
		return
	}
	if body.ClientRequestID != nil {
		if !validCommentClientRequestID(*body.ClientRequestID) {
			jsonError(w, "invalid client_request_id", http.StatusBadRequest)
			return
		}
		if body.Visibility != CommentVisibilityInternal {
			jsonError(w, "client_request_id is allowed only for internal comments", http.StatusBadRequest)
			return
		}
	}

	user := auth.GetUser(r)
	var authorID *int64
	if user != nil {
		authorID = &user.ID
	}
	if body.ClientRequestID != nil && authorID == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("CreateComment: begin tx: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if !issueExistsActiveTx(tx, issueID) {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}

	res, err := tx.ExecContext(r.Context(), `
		INSERT INTO comments(issue_id, author_id, body, visibility, client_request_id)
		VALUES(?, ?, ?, ?, ?)
	`, issueID, authorID, body.Body, body.Visibility, body.ClientRequestID)
	if err != nil {
		if body.ClientRequestID != nil && isCommentClientRequestConflict(err) {
			prior, lookupErr := commentByClientRequestIDTx(tx, *authorID, *body.ClientRequestID)
			if lookupErr != nil {
				log.Printf("CreateComment: exact-once lookup author_id=%d: %v", *authorID, lookupErr)
				jsonError(w, "internal error", http.StatusInternalServerError)
				return
			}
			if prior.IssueID != issueID || prior.Body != body.Body || prior.Visibility != CommentVisibilityInternal {
				problemJSON(w, r, ProblemDetails{
					Status: http.StatusConflict, Code: "client_request_id_conflict",
					Detail: "client_request_id was reused with a different issue, body, or visibility",
				})
				return
			}
			if err := tx.Commit(); err != nil {
				log.Printf("CreateComment: exact-once replay commit: %v", err)
				jsonError(w, "internal error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("X-PAIMOS-Idempotency-Replay", "true")
			writeComment(w, http.StatusOK, prior)
			return
		}
		log.Printf("CreateComment: issue_id=%d author_id=%v err=%v", issueID, authorID, err)
		jsonError(w, "insert failed", http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()

	before := commentMutationSnapshot{ID: id}
	after, err := fetchCommentMutationSnapshotTx(tx, id)
	if err != nil {
		log.Printf("CreateComment: snapshot id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID:    requestIDFromRequest(r),
		UserID:       authorID,
		SessionID:    sessionIDFromRequest(r),
		AgentName:    agentNameFromRequest(r),
		MutationType: mutationTypeForRequest(r, "issue.comment.create"),
		SubjectType:  "comment",
		SubjectID:    id,
		InverseOp: InverseOp{
			Method: http.MethodDelete,
			Path:   fmt.Sprintf("/comments/%d", id),
		},
		BeforeState: before,
		AfterState:  after,
		Undoable:    true,
	}); err != nil {
		log.Printf("CreateComment: recordMutation: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	c, err := commentByIDTx(tx, id)
	if err != nil {
		log.Printf("CreateComment: response id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("CreateComment: commit: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeComment(w, http.StatusCreated, c)
}

func validCommentClientRequestID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:-", rune(char)) {
			continue
		}
		return false
	}
	return true
}

func isCommentClientRequestConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(),
		"UNIQUE constraint failed: comments.author_id, comments.client_request_id")
}

func commentByClientRequestIDTx(tx *sql.Tx, authorID int64, clientRequestID string) (Comment, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM comments WHERE author_id=? AND client_request_id=?`,
		authorID, clientRequestID).Scan(&id)
	if err != nil {
		return Comment{}, err
	}
	return commentByIDTx(tx, id)
}

func commentByIDTx(tx *sql.Tx, id int64) (Comment, error) {
	var c Comment
	err := tx.QueryRow(`
		SELECT c.id, c.issue_id, c.author_id, COALESCE(NULLIF(u.nickname,''), u.username), u.avatar_path,
		       c.body, c.visibility, c.client_request_id, c.created_at
		FROM comments c LEFT JOIN users u ON u.id = c.author_id
		WHERE c.id = ?
	`, id).Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Author, &c.AvatarPath, &c.Body, &c.Visibility,
		&c.ClientRequestID, &c.CreatedAt)
	if err != nil {
		return Comment{}, err
	}
	return c, nil
}

func writeComment(w http.ResponseWriter, status int, comment Comment) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(comment)
}

// PATCH /api/comments/{id}  { "visibility": "internal" | "external" }
//
// PAI-475: lets the comment author or an admin flip a comment's
// visibility post-hoc. Only the visibility field is mutable here — the
// body stays immutable (the audit trail is cleaner that way; if you
// want to amend wording, delete and re-post).
//
// Authorization: comment author, or admin. The route is also gated by
// RequireCommentEdit at the middleware layer for project-level access.
func UpdateCommentVisibility(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Visibility string `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !isValidCommentVisibility(body.Visibility) {
		jsonError(w, "invalid visibility", http.StatusBadRequest)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("UpdateCommentVisibility: begin tx: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	before, err := fetchCommentMutationSnapshotTx(tx, id)
	if err != nil {
		log.Printf("UpdateCommentVisibility: snapshot id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !before.Exists {
		jsonError(w, "comment not found", http.StatusNotFound)
		return
	}

	isOwner := before.AuthorID != nil && *before.AuthorID == user.ID
	if !isOwner && !auth.IsAdmin(user) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	if before.ClientRequestID != nil && body.Visibility != CommentVisibilityInternal {
		jsonError(w, "client-request comments must remain internal", http.StatusBadRequest)
		return
	}

	// No-op when the visibility already matches — return 200 with the
	// current state rather than write a noisy mutation_log row.
	if before.Visibility == body.Visibility {
		if err := tx.Commit(); err != nil {
			log.Printf("UpdateCommentVisibility: commit no-op: %v", err)
		}
		jsonOK(w, map[string]any{"id": id, "visibility": body.Visibility})
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE comments SET visibility = ? WHERE id = ?
	`, body.Visibility, id); err != nil {
		log.Printf("UpdateCommentVisibility: update id=%d: %v", id, err)
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}

	after, err := fetchCommentMutationSnapshotTx(tx, id)
	if err != nil {
		log.Printf("UpdateCommentVisibility: after snapshot id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	userID := user.ID
	if _, err := recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID:    requestIDFromRequest(r),
		UserID:       &userID,
		SessionID:    sessionIDFromRequest(r),
		AgentName:    agentNameFromRequest(r),
		MutationType: mutationTypeForRequest(r, "issue.comment.visibility.change"),
		SubjectType:  "comment",
		SubjectID:    id,
		// Reuse the PUT-comment-snapshot restore path that DeleteComment
		// also uses. applyCommentSnapshotTx writes every field
		// (including visibility), so the inverse of a flip is a normal
		// snapshot restore.
		InverseOp: InverseOp{
			Method: http.MethodPut,
			Path:   fmt.Sprintf("/comments/%d", id),
			Body:   before,
		},
		BeforeState: before,
		AfterState:  after,
		Undoable:    true,
	}); err != nil {
		log.Printf("UpdateCommentVisibility: recordMutation: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("UpdateCommentVisibility: commit: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{"id": id, "visibility": body.Visibility})
}

// DELETE /api/comments/{id}  — own comment or admin
func DeleteComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("DeleteComment: begin tx: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	before, err := fetchCommentMutationSnapshotTx(tx, id)
	if err != nil {
		log.Printf("DeleteComment: snapshot id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !before.Exists {
		jsonError(w, "comment not found", http.StatusNotFound)
		return
	}

	isOwner := before.AuthorID != nil && *before.AuthorID == user.ID
	if !isOwner && !auth.IsAdmin(user) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	if _, err := tx.ExecContext(r.Context(), "DELETE FROM comments WHERE id=?", id); err != nil {
		log.Printf("DeleteComment: id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	after, err := fetchCommentMutationSnapshotTx(tx, id)
	if err != nil {
		log.Printf("DeleteComment: after snapshot id=%d: %v", id, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	userID := user.ID
	if _, err := recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID:    requestIDFromRequest(r),
		UserID:       &userID,
		SessionID:    sessionIDFromRequest(r),
		AgentName:    agentNameFromRequest(r),
		MutationType: mutationTypeForRequest(r, "issue.comment.delete"),
		SubjectType:  "comment",
		SubjectID:    id,
		InverseOp: InverseOp{
			Method: http.MethodPut,
			Path:   fmt.Sprintf("/comments/%d", id),
			Body:   before,
		},
		BeforeState: before,
		AfterState:  after,
		Undoable:    true,
	}); err != nil {
		log.Printf("DeleteComment: recordMutation: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("DeleteComment: commit: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
