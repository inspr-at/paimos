// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

type createStructuredKnowledgeLinkRequest struct {
	Relation      string `json:"relation"`
	TargetIssueID int64  `json:"target_issue_id"`
}

func loadStructuredKnowledgeLinksTx(ctx context.Context, tx *sql.Tx, entries []models.StructuredKnowledgeEntry) (map[int64][]models.StructuredKnowledgeLink, error) {
	out := make(map[int64][]models.StructuredKnowledgeLink, len(entries))
	if len(entries) == 0 {
		return out, nil
	}
	ids := make([]any, 0, len(entries))
	focal := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.KnowledgeID)
		focal[entry.KnowledgeID] = struct{}{}
		out[entry.KnowledgeID] = []models.StructuredKnowledgeLink{}
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := append(append([]any{}, ids...), ids...)
	rows, err := tx.QueryContext(ctx, `SELECT l.link_id,l.source_knowledge_id,l.target_issue_id,l.canonical_kind,
		si.type,COALESCE(si.slug,''),si.title,CASE WHEN ssk.knowledge_id IS NULL THEN 0 ELSE 1 END,
		ti.type,COALESCE(ti.slug,''),ti.title,CASE WHEN tsk.knowledge_id IS NULL THEN 0 ELSE 1 END
		FROM structured_knowledge_links l
		JOIN issues si ON si.id=l.source_knowledge_id AND si.deleted_at IS NULL
		JOIN issues ti ON ti.id=l.target_issue_id AND ti.deleted_at IS NULL
		LEFT JOIN structured_knowledge_entries ssk ON ssk.knowledge_id=si.id
		LEFT JOIN structured_knowledge_entries tsk ON tsk.knowledge_id=ti.id
		WHERE l.source_knowledge_id IN (`+marks+`) OR l.target_issue_id IN (`+marks+`)
		ORDER BY l.link_id`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var linkID, sourceID, targetID int64
		var kind, sourceType, sourceSlug, sourceTitle, targetType, targetSlug, targetTitle string
		var sourceStructured, targetStructured int
		if err := rows.Scan(&linkID, &sourceID, &targetID, &kind,
			&sourceType, &sourceSlug, &sourceTitle, &sourceStructured,
			&targetType, &targetSlug, &targetTitle, &targetStructured); err != nil {
			return out, err
		}
		if _, ok := focal[sourceID]; ok {
			out[sourceID] = append(out[sourceID], models.StructuredKnowledgeLink{
				LinkID: linkID, Relation: kind, TargetIssueID: targetID, TargetKnowledge: targetStructured == 1,
				TargetType: targetType, TargetSlug: targetSlug, TargetTitle: targetTitle,
			})
		}
		if _, ok := focal[targetID]; ok && (kind == "parent" || kind == "see_also") {
			relation := "child"
			if kind == "see_also" {
				relation = kind
			}
			out[targetID] = append(out[targetID], models.StructuredKnowledgeLink{
				LinkID: linkID, Relation: relation, TargetIssueID: sourceID, TargetKnowledge: sourceStructured == 1,
				TargetType: sourceType, TargetSlug: sourceSlug, TargetTitle: sourceTitle,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			if out[id][i].Relation != out[id][j].Relation {
				return out[id][i].Relation < out[id][j].Relation
			}
			if out[id][i].TargetIssueID != out[id][j].TargetIssueID {
				return out[id][i].TargetIssueID < out[id][j].TargetIssueID
			}
			return out[id][i].LinkID < out[id][j].LinkID
		})
	}
	return out, nil
}

func CreateStructuredKnowledgeLinkV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	knowledgeID, ok := structuredKnowledgeID(w, r)
	if !ok {
		return
	}
	var body createStructuredKnowledgeLinkRequest
	if !decodeStructuredKnowledgeJSON(w, r, 4096, &body) {
		return
	}
	body.Relation = strings.ToLower(strings.TrimSpace(body.Relation))
	if body.TargetIssueID <= 0 || body.TargetIssueID == knowledgeID {
		jsonError(w, "a different positive target_issue_id is required", http.StatusBadRequest)
		return
	}
	switch body.Relation {
	case "parent", "child", "about", "see_also", "supersedes":
	default:
		jsonError(w, "relation must be parent, child, about, see_also, or supersedes", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "structured knowledge link failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	_, principal, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, true)
	if err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	var focalExists, targetExists, targetStructured int
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(
		SELECT 1 FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		WHERE sk.knowledge_id=? AND sk.level='project' AND sk.origin_project_id=? AND i.project_id=? AND i.deleted_at IS NULL)`,
		knowledgeID, projectID, projectID).Scan(&focalExists); err != nil {
		jsonError(w, "structured knowledge link failed", http.StatusInternalServerError)
		return
	}
	if focalExists != 1 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM issues WHERE id=? AND project_id=? AND deleted_at IS NULL),
		EXISTS(SELECT 1 FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		 WHERE sk.knowledge_id=? AND sk.level='project' AND sk.origin_project_id=? AND i.project_id=? AND i.deleted_at IS NULL)`,
		body.TargetIssueID, projectID, body.TargetIssueID, projectID, projectID).Scan(&targetExists, &targetStructured); err != nil {
		jsonError(w, "structured knowledge link failed", http.StatusInternalServerError)
		return
	}
	if targetExists != 1 {
		jsonError(w, "target not found", http.StatusNotFound)
		return
	}
	if body.Relation != "about" && targetStructured != 1 {
		jsonError(w, "this relation requires a structured knowledge target", http.StatusUnprocessableEntity)
		return
	}

	sourceID, targetID, canonicalKind := knowledgeID, body.TargetIssueID, body.Relation
	switch body.Relation {
	case "child":
		sourceID, targetID, canonicalKind = body.TargetIssueID, knowledgeID, "parent"
	case "see_also":
		if sourceID > targetID {
			sourceID, targetID = targetID, sourceID
		}
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO structured_knowledge_links(
		source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id)
		VALUES(?,?,?,?)`, sourceID, targetID, canonicalKind, principal.ActorUserID())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			jsonError(w, "structured knowledge link already exists", http.StatusConflict)
			return
		}
		if strings.Contains(err.Error(), "scope") || strings.Contains(err.Error(), "structured knowledge") || strings.Contains(err.Error(), "cycle") {
			jsonError(w, "structured knowledge link violates scope or graph rules", http.StatusUnprocessableEntity)
			return
		}
		jsonError(w, "structured knowledge link failed", http.StatusInternalServerError)
		return
	}
	linkID, _ := result.LastInsertId()
	if err := tx.Commit(); err != nil {
		jsonError(w, "structured knowledge link failed", http.StatusInternalServerError)
		return
	}
	entry, err := loadStructuredKnowledgeEntry(projectID, knowledgeID)
	if err != nil {
		jsonError(w, "structured knowledge read failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{"link_id": linkID, "entry": entry})
}

func DeleteStructuredKnowledgeLinkV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	knowledgeID, ok := structuredKnowledgeID(w, r)
	if !ok {
		return
	}
	linkID, err := strconv.ParseInt(chi.URLParam(r, "linkID"), 10, 64)
	if err != nil || linkID <= 0 {
		jsonError(w, "invalid link id", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "structured knowledge unlink failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if _, _, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, true); err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	result, err := tx.ExecContext(r.Context(), `DELETE FROM structured_knowledge_links
		WHERE link_id=? AND (
		 source_knowledge_id=? OR
		 (target_issue_id=? AND canonical_kind IN ('parent','see_also'))
		) AND EXISTS(
		 SELECT 1 FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		 WHERE sk.knowledge_id=? AND sk.level='project' AND sk.origin_project_id=? AND i.project_id=? AND i.deleted_at IS NULL
		)`, linkID, knowledgeID, knowledgeID, knowledgeID, projectID, projectID)
	if err != nil {
		jsonError(w, "structured knowledge unlink failed", http.StatusInternalServerError)
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "structured knowledge unlink failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
