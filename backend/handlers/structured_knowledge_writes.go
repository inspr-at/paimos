// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers/knowledge"
	"github.com/inspr-at/paimos/backend/models"
)

type adoptStructuredKnowledgeRequest struct {
	Purpose string `json:"purpose"`
}

func ValidateStructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	var body structuredKnowledgeCandidateRequest
	if !decodeStructuredKnowledgeJSON(w, r, structuredKnowledgeJSONWireLimit(structuredKnowledgeProposalMaxBytes), &body) {
		return
	}
	if err := normalizeStructuredKnowledgeCandidate(&body); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len([]byte(body.ShortBody)) > structuredKnowledgeProposalMaxBytes {
		jsonError(w, "candidate body exceeds 65536 UTF-8 bytes", http.StatusRequestEntityTooLarge)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		jsonError(w, "validation failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if _, _, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, false); err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	validation, err := structuredKnowledgeValidationForTx(r.Context(), tx, projectID, 0,
		body.Title, body.ShortBody, structuredKnowledgeShortBodyLimitBytes)
	if err != nil {
		jsonError(w, "validation failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "validation failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, validation)
}

func RememberStructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	var body structuredKnowledgeCandidateRequest
	if !decodeStructuredKnowledgeJSON(w, r, structuredKnowledgeJSONWireLimit(structuredKnowledgeProposalMaxBytes), &body) {
		return
	}
	if err := normalizeStructuredKnowledgeCandidate(&body); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len([]byte(body.ShortBody)) > structuredKnowledgeProposalMaxBytes {
		jsonError(w, "candidate body exceeds 65536 UTF-8 bytes", http.StatusRequestEntityTooLarge)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "remember proposal failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	_, principal, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, true, false)
	if err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	compactID, err := ensureStructuredCompactTx(r.Context(), tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Compact product session is not configured", http.StatusConflict)
		return
	}
	if err != nil {
		jsonError(w, "remember proposal failed", http.StatusInternalServerError)
		return
	}
	proposalID := uuid.NewString()
	now := structuredKnowledgeNow()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO structured_knowledge_proposals(
		proposal_id,project_id,product_session_id,source_kind,proposed_type,slug,title,purpose,candidate_body,
		created_by_user_id,created_at,updated_at)
		VALUES(?,?,?,'remember',?,?,?,?,?,?,?,?)`, proposalID, projectID, compactID,
		knowledgeIssueType(body.Type), body.Slug, body.Title, body.Purpose, body.ShortBody,
		principal.ActorUserID(), now, now)
	if err != nil {
		structuredKnowledgeProblem(w, err, "remember proposal")
		return
	}
	validation, err := structuredKnowledgeValidationForTx(r.Context(), tx, projectID, 0,
		body.Title, body.ShortBody, structuredKnowledgeShortBodyLimitBytes)
	if err != nil {
		jsonError(w, "remember proposal failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "remember proposal failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, models.StructuredKnowledgeProposal{
		ProposalID: proposalID, SourceKind: "remember", ProductSessionID: compactID,
		Type: knowledgeIssueType(body.Type), Slug: body.Slug, Title: body.Title,
		Purpose: body.Purpose, CandidateBody: body.ShortBody, State: "proposed",
		Validation: validation, CreatedAt: now, UpdatedAt: now,
	})
}

func BindStructuredKnowledgeCompactV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	var body bindKnowledgeCompactRequest
	if !decodeStructuredKnowledgeJSON(w, r, 4096, &body) {
		return
	}
	if _, err := uuid.Parse(body.ProductSessionID); err != nil || body.ExpectedRevision < 0 {
		jsonError(w, "valid product_session_id and non-negative expected_revision required", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "Compact binding failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	_, principal, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, true)
	if err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	paimosTarget, err := structuredKnowledgePaimosProductSessionTx(r.Context(), tx, projectID, body.ProductSessionID)
	if err != nil {
		jsonError(w, "Compact binding failed", http.StatusInternalServerError)
		return
	}
	if !paimosTarget {
		jsonError(w, "Compact must be a Paimos product session in this project", http.StatusUnprocessableEntity)
		return
	}
	now := structuredKnowledgeNow()
	var revision int64
	err = tx.QueryRowContext(r.Context(), `SELECT revision FROM knowledge_compact_sessions WHERE project_id=?`, projectID).Scan(&revision)
	switch {
	case errors.Is(err, sql.ErrNoRows) && body.ExpectedRevision == 0:
		_, err = tx.ExecContext(r.Context(), `INSERT INTO knowledge_compact_sessions(
			project_id,product_session_id,revision,created_by_user_id,updated_by_user_id,created_at,updated_at)
			VALUES(?,?,1,?,?,?,?)`, projectID, body.ProductSessionID, principal.ActorUserID(), principal.ActorUserID(), now, now)
		revision = 1
	case errors.Is(err, sql.ErrNoRows):
		jsonError(w, "Compact binding revision conflict", http.StatusConflict)
		return
	case err != nil:
		jsonError(w, "Compact binding failed", http.StatusInternalServerError)
		return
	case revision != body.ExpectedRevision:
		jsonError(w, "Compact binding revision conflict", http.StatusConflict)
		return
	default:
		result, updateErr := tx.ExecContext(r.Context(), `UPDATE knowledge_compact_sessions
			SET product_session_id=?,revision=revision+1,updated_by_user_id=?,updated_at=?
			WHERE project_id=? AND revision=?`, body.ProductSessionID, principal.ActorUserID(), now, projectID, revision)
		if updateErr != nil {
			err = updateErr
		} else if changed, _ := result.RowsAffected(); changed != 1 {
			jsonError(w, "Compact binding revision conflict", http.StatusConflict)
			return
		}
		revision++
	}
	if err != nil {
		if strings.Contains(err.Error(), "product session") || strings.Contains(err.Error(), "FOREIGN KEY") {
			jsonError(w, "Compact must be a Paimos product session in this project", http.StatusUnprocessableEntity)
			return
		}
		structuredKnowledgeProblem(w, err, "Compact binding")
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "Compact binding failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"project_id": projectID, "product_session_id": body.ProductSessionID, "revision": revision})
}

func CreateStructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	var body createStructuredKnowledgeRequest
	if !decodeStructuredKnowledgeJSON(w, r, structuredKnowledgeJSONWireLimit(structuredKnowledgeShortBodyLimitBytes), &body) {
		return
	}
	if err := normalizeStructuredKnowledgeCandidate(&body.structuredKnowledgeCandidateRequest); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len([]byte(body.ShortBody)) > structuredKnowledgeShortBodyLimitBytes {
		jsonError(w, "short_body exceeds the product limit", http.StatusUnprocessableEntity)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "structured knowledge create failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	actorUser, principal, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, true)
	if err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	compactID, err := ensureStructuredCompactTx(r.Context(), tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Compact product session is not configured", http.StatusConflict)
		return
	}
	if err != nil {
		jsonError(w, "structured knowledge create failed", http.StatusInternalServerError)
		return
	}
	if body.ProposalID != nil {
		var proposal struct {
			productSessionID, typeName, slug, title, purpose, candidateBody, state string
		}
		err = tx.QueryRowContext(r.Context(), `SELECT product_session_id,proposed_type,slug,title,purpose,candidate_body,state
			FROM structured_knowledge_proposals WHERE proposal_id=? AND project_id=?`, *body.ProposalID, projectID).Scan(
			&proposal.productSessionID, &proposal.typeName, &proposal.slug, &proposal.title,
			&proposal.purpose, &proposal.candidateBody, &proposal.state)
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "proposal not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "structured knowledge create failed", http.StatusInternalServerError)
			return
		}
		if proposal.state != "proposed" || proposal.productSessionID != compactID || proposal.typeName != knowledgeIssueType(body.Type) ||
			proposal.slug != body.Slug || proposal.title != body.Title || proposal.purpose != body.Purpose || proposal.candidateBody != body.ShortBody {
			jsonError(w, "proposal no longer matches the reviewed candidate", http.StatusConflict)
			return
		}
	}
	nextNumber, err := db.NextIssueNumber(r.Context(), tx, projectID)
	if err != nil {
		jsonError(w, "structured knowledge create failed", http.StatusInternalServerError)
		return
	}
	now := structuredKnowledgeNow()
	result, err := tx.ExecContext(r.Context(), `INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,?,?,?,?,'backlog','medium',?,?,'{}',?)`, projectID, nextNumber, knowledgeIssueType(body.Type),
		body.Title, body.ShortBody, principal.ActorUserID(), body.Slug, now)
	if err != nil {
		structuredKnowledgeProblem(w, err, "structured knowledge create")
		return
	}
	knowledgeID, _ := result.LastInsertId()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,?,?,?,?,?,?,?)`, knowledgeID, projectID, body.Purpose, compactID, 1,
		principal.ActorUserID(), principal.ActorUserID(), now, now)
	if err != nil {
		structuredKnowledgeProblem(w, err, "structured knowledge create")
		return
	}
	if body.ProposalID != nil {
		updated, err := tx.ExecContext(r.Context(), `UPDATE structured_knowledge_proposals
			SET state='promoted',promoted_knowledge_id=?,updated_at=?
			WHERE proposal_id=? AND project_id=? AND state='proposed'`, knowledgeID, now, *body.ProposalID, projectID)
		if err != nil {
			structuredKnowledgeProblem(w, err, "proposal promotion")
			return
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			jsonError(w, "proposal state changed concurrently", http.StatusConflict)
			return
		}
	}
	if err := structuredKnowledgeCreateMutationTx(r, tx, knowledgeID, principal.ActorUserID()); err != nil {
		jsonError(w, "structured knowledge create failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		structuredKnowledgeProblem(w, err, "structured knowledge create")
		return
	}
	if issue := getIssueByID(knowledgeID); issue != nil {
		saveSnapshot(issue, actorUser, r)
	}
	EvaluateSystemTags(knowledgeID)
	entry, err := loadStructuredKnowledgeEntry(projectID, knowledgeID)
	if err != nil {
		jsonError(w, "structured knowledge read failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, entry)
}

func AdoptStructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	knowledgeID, ok := structuredKnowledgeID(w, r)
	if !ok {
		return
	}
	var body adoptStructuredKnowledgeRequest
	if !decodeStructuredKnowledgeJSON(w, r, 4096, &body) {
		return
	}
	body.Purpose = strings.TrimSpace(body.Purpose)
	if len([]byte(body.Purpose)) < 1 || len([]byte(body.Purpose)) > 400 || !structuredKnowledgeSafeLine(body.Purpose) {
		jsonError(w, "purpose must be 1 to 400 UTF-8 bytes", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "legacy adoption failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	_, principal, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, true)
	if err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	compactID, err := ensureStructuredCompactTx(r.Context(), tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Compact product session is not configured", http.StatusConflict)
		return
	}
	if err != nil {
		jsonError(w, "legacy adoption failed", http.StatusInternalServerError)
		return
	}
	var slug, title, shortBody, rawMetadata string
	err = tx.QueryRowContext(r.Context(), `SELECT slug,title,COALESCE(description,''),COALESCE(category_metadata,'')
		FROM issues WHERE id=? AND project_id=? AND deleted_at IS NULL AND slug IS NOT NULL
		AND type IN ('memory','runbook','external_system','related_project','guideline')`, knowledgeID, projectID).Scan(&slug, &title, &shortBody, &rawMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "legacy adoption failed", http.StatusInternalServerError)
		return
	}
	if err := validateStructuredKnowledgeLegacyAdoptionCandidate(slug, title, shortBody, rawMetadata); err != nil {
		jsonError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var relationCount int
	err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM issue_relations WHERE (source_id=? OR target_id=?)
		AND type IN ('parent','applies_to_memory','depends_on','impacts','follows_from','blocks','related')`, knowledgeID, knowledgeID).Scan(&relationCount)
	if err != nil {
		jsonError(w, "legacy adoption failed", http.StatusInternalServerError)
		return
	}
	if relationCount != 0 {
		jsonError(w, "legacy graph links require an explicit canonical remap before adoption", http.StatusConflict)
		return
	}
	now := structuredKnowledgeNow()
	if _, err := tx.ExecContext(r.Context(), `UPDATE issues SET updated_at=? WHERE id=?`, now, knowledgeID); err != nil {
		jsonError(w, "legacy adoption failed", http.StatusInternalServerError)
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,?,?,?,?,?,?,?)`, knowledgeID, projectID, body.Purpose, compactID, 1,
		principal.ActorUserID(), principal.ActorUserID(), now, now)
	if err != nil {
		structuredKnowledgeProblem(w, err, "legacy adoption")
		return
	}
	if err := tx.Commit(); err != nil {
		structuredKnowledgeProblem(w, err, "legacy adoption")
		return
	}
	entry, err := loadStructuredKnowledgeEntry(projectID, knowledgeID)
	if err != nil {
		jsonError(w, "structured knowledge read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, entry)
}

func validateStructuredKnowledgeLegacyAdoptionCandidate(slug, title, shortBody, rawMetadata string) error {
	if knowledge.ValidateSlug(slug) != nil || len([]byte(title)) < 1 || len([]byte(title)) > 240 ||
		strings.TrimSpace(title) != title || !structuredKnowledgeSafeLine(title) || strings.TrimSpace(shortBody) == "" ||
		strings.ContainsRune(shortBody, 0) || len([]byte(shortBody)) > structuredKnowledgeShortBodyLimitBytes {
		return errors.New("legacy identity and body must be explicitly normalized before adoption")
	}
	if rawMetadata != "" && rawMetadata != "{}" {
		return errors.New("legacy metadata must be explicitly cleared before adoption")
	}
	return nil
}

func UpdateStructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	knowledgeID, ok := structuredKnowledgeID(w, r)
	if !ok {
		return
	}
	var body updateStructuredKnowledgeRequest
	if !decodeStructuredKnowledgeJSON(w, r, structuredKnowledgeJSONWireLimit(structuredKnowledgeShortBodyLimitBytes), &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	body.Purpose = strings.TrimSpace(body.Purpose)
	if body.ExpectedRevision <= 0 || len([]byte(body.Title)) < 1 || len([]byte(body.Title)) > 240 || !structuredKnowledgeSafeLine(body.Title) ||
		len([]byte(body.Purpose)) < 1 || len([]byte(body.Purpose)) > 400 || strings.TrimSpace(body.ShortBody) == "" ||
		!structuredKnowledgeSafeLine(body.Purpose) || strings.ContainsRune(body.ShortBody, 0) || len([]byte(body.ShortBody)) > structuredKnowledgeShortBodyLimitBytes {
		jsonError(w, "expected_revision and bounded title, purpose, short_body are required", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "structured knowledge update failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	actorUser, principal, err := reauthorizeStructuredKnowledgeProjectTx(r.Context(), tx, r, projectID, false, true)
	if err != nil {
		writeStructuredKnowledgeAuthorityError(w, err)
		return
	}
	var revision int64
	err = tx.QueryRowContext(r.Context(), `SELECT sk.revision FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		WHERE sk.knowledge_id=? AND sk.level='project' AND sk.origin_project_id=? AND i.project_id=? AND i.deleted_at IS NULL`,
		knowledgeID, projectID, projectID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "structured knowledge update failed", http.StatusInternalServerError)
		return
	}
	if revision != body.ExpectedRevision {
		jsonError(w, "structured knowledge revision conflict", http.StatusConflict)
		return
	}
	before, err := fetchIssueMutationSnapshotTx(tx, knowledgeID)
	if err != nil {
		jsonError(w, "structured knowledge update failed", http.StatusInternalServerError)
		return
	}
	now := structuredKnowledgeNow()
	result, err := tx.ExecContext(r.Context(), `UPDATE structured_knowledge_entries
		SET purpose=?,revision=revision+1,updated_by_user_id=?,updated_at=?
		WHERE knowledge_id=? AND revision=?`, body.Purpose, principal.ActorUserID(), now, knowledgeID, revision)
	if err != nil {
		structuredKnowledgeProblem(w, err, "structured knowledge update")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		jsonError(w, "structured knowledge revision conflict", http.StatusConflict)
		return
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE issues SET title=?,description=?,updated_at=? WHERE id=?`, body.Title, body.ShortBody, now, knowledgeID)
	if err != nil {
		structuredKnowledgeProblem(w, err, "structured knowledge update")
		return
	}
	after, err := fetchIssueMutationSnapshotTx(tx, knowledgeID)
	if err != nil {
		jsonError(w, "structured knowledge update failed", http.StatusInternalServerError)
		return
	}
	userID := principal.ActorUserID()
	_, err = recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID: requestIDFromRequest(r), UserID: &userID, SessionID: sessionIDFromRequest(r),
		AgentName: agentNameFromRequest(r), MutationType: mutationTypeForRequest(r, "issue.update"),
		SubjectType: "issue", SubjectID: knowledgeID,
		InverseOp:   InverseOp{},
		BeforeState: before, AfterState: after, Undoable: false,
	})
	if err != nil {
		jsonError(w, "structured knowledge update failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		structuredKnowledgeProblem(w, err, "structured knowledge update")
		return
	}
	if issue := getIssueByID(knowledgeID); issue != nil {
		saveSnapshot(issue, actorUser, r)
	}
	entry, err := loadStructuredKnowledgeEntry(projectID, knowledgeID)
	if err != nil {
		jsonError(w, "structured knowledge read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, entry)
}

func loadStructuredKnowledgeEntry(projectID, knowledgeID int64) (models.StructuredKnowledgeEntry, error) {
	tx, err := db.DB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.StructuredKnowledgeEntry{}, err
	}
	defer tx.Rollback()
	entry, err := loadStructuredKnowledgeEntryProjectionTx(context.Background(), tx, projectID, knowledgeID)
	if err != nil {
		return entry, err
	}
	if err := tx.Commit(); err != nil {
		return entry, err
	}
	return entry, nil
}

func loadStructuredKnowledgeEntryProjectionTx(ctx context.Context, tx *sql.Tx, projectID, knowledgeID int64) (models.StructuredKnowledgeEntry, error) {
	var entry models.StructuredKnowledgeEntry
	err := tx.QueryRowContext(ctx, `SELECT sk.knowledge_id,sk.level,i.type,COALESCE(i.slug,''),i.title,sk.purpose,
		COALESCE(i.description,''),sk.authored_product_session_id,sk.revision,sk.created_at,sk.updated_at
		FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		WHERE sk.knowledge_id=? AND sk.origin_project_id=? AND i.deleted_at IS NULL`, knowledgeID, projectID).Scan(
		&entry.KnowledgeID, &entry.Level, &entry.Type, &entry.Slug, &entry.Title, &entry.Purpose,
		&entry.ShortBody, &entry.AuthoredProductSessionID, &entry.Revision, &entry.CreatedAt, &entry.UpdatedAt)
	if err != nil {
		return entry, err
	}
	entry.Validation, err = structuredKnowledgeValidationForTx(ctx, tx, projectID, knowledgeID,
		entry.Title, entry.ShortBody, structuredKnowledgeShortBodyLimitBytes)
	if err != nil {
		return entry, err
	}
	links, err := loadStructuredKnowledgeLinksTx(ctx, tx, []models.StructuredKnowledgeEntry{entry})
	if err != nil {
		return entry, err
	}
	entry.Links = links[knowledgeID]
	if entry.Links == nil {
		entry.Links = []models.StructuredKnowledgeLink{}
	}
	return entry, nil
}
