// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers/knowledge"
	"github.com/inspr-at/paimos/backend/models"
)

const structuredKnowledgeSchemaVersion = 1

type structuredKnowledgeCandidateRequest struct {
	Type      string `json:"type"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Purpose   string `json:"purpose"`
	ShortBody string `json:"short_body"`
}

type createStructuredKnowledgeRequest struct {
	structuredKnowledgeCandidateRequest
	ProposalID *string `json:"proposal_id"`
}

type updateStructuredKnowledgeRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Title            string `json:"title"`
	Purpose          string `json:"purpose"`
	ShortBody        string `json:"short_body"`
}

type bindKnowledgeCompactRequest struct {
	ProductSessionID string `json:"product_session_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func RegisterStructuredKnowledgeRoutes(r chi.Router) {
	r.With(auth.RequireProjectView).Get("/projects/{id}/structured-knowledge/v1", StructuredKnowledgeV1)
	r.With(auth.RequireProjectView).Post("/projects/{id}/structured-knowledge/v1/validate", ValidateStructuredKnowledgeV1)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/structured-knowledge/v1/remember", RememberStructuredKnowledgeV1)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/structured-knowledge/v1/compact", BindStructuredKnowledgeCompactV1)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/structured-knowledge/v1/entries", CreateStructuredKnowledgeV1)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/adopt", AdoptStructuredKnowledgeV1)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}", UpdateStructuredKnowledgeV1)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/links", CreateStructuredKnowledgeLinkV1)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/links/{linkID}", DeleteStructuredKnowledgeLinkV1)
	r.Post("/structured-knowledge/v1/entries/{knowledgeID}/promote", PromoteStructuredKnowledgeV1)
}

func structuredKnowledgeNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
}

func decodeStructuredKnowledgeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) bool {
	reader := http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		jsonError(w, "invalid structured knowledge body", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		jsonError(w, "invalid structured knowledge body", http.StatusBadRequest)
		return false
	}
	return true
}

func structuredKnowledgeProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func structuredKnowledgeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "knowledgeID"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid knowledge id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func structuredKnowledgeNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

var (
	errStructuredKnowledgeConcealed = errors.New("structured knowledge project not found")
	errStructuredKnowledgeForbidden = errors.New("structured knowledge project forbidden")
)

func reauthorizeStructuredKnowledgeProjectTx(
	ctx context.Context,
	tx *sql.Tx,
	r *http.Request,
	projectID int64,
	requireEdit, requireAdmin bool,
) (*models.User, auth.Principal, error) {
	user, principal, err := auth.ReauthorizeRequestPrincipalTx(ctx, tx, r, time.Now().UTC())
	if err != nil || user == nil || !sessionHomeProjectViewTx(ctx, tx, user, projectID) {
		return nil, principal, errStructuredKnowledgeConcealed
	}
	if requireAdmin && !auth.IsAdmin(user) {
		return nil, principal, errStructuredKnowledgeForbidden
	}
	if requireEdit {
		allowed, err := canEditProjectTx(ctx, tx, user, projectID)
		if err != nil {
			return nil, principal, err
		}
		if !allowed {
			return nil, principal, errStructuredKnowledgeForbidden
		}
	}
	if principal.ActorUserID() <= 0 {
		return nil, principal, errStructuredKnowledgeConcealed
	}
	return user, principal, nil
}

func writeStructuredKnowledgeAuthorityError(w http.ResponseWriter, err error) {
	if errors.Is(err, errStructuredKnowledgeConcealed) {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, errStructuredKnowledgeForbidden) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	jsonError(w, "authorization failed", http.StatusInternalServerError)
}

func normalizeStructuredKnowledgeCandidate(candidate *structuredKnowledgeCandidateRequest) error {
	candidate.Type = strings.TrimSpace(candidate.Type)
	candidate.Slug = strings.TrimSpace(candidate.Slug)
	candidate.Title = strings.TrimSpace(candidate.Title)
	candidate.Purpose = strings.TrimSpace(candidate.Purpose)
	if _, err := structuredKnowledgeModule(candidate.Type); err != nil {
		return err
	}
	if err := knowledge.ValidateSlug(candidate.Slug); err != nil {
		return err
	}
	if len([]byte(candidate.Title)) < 1 || len([]byte(candidate.Title)) > 240 {
		return errors.New("title must be 1 to 240 UTF-8 bytes")
	}
	if !structuredKnowledgeSafeLine(candidate.Title) {
		return errors.New("title must be one control-free line")
	}
	if len([]byte(candidate.Purpose)) < 1 || len([]byte(candidate.Purpose)) > 400 {
		return errors.New("purpose must be 1 to 400 UTF-8 bytes")
	}
	if !structuredKnowledgeSafeLine(candidate.Purpose) {
		return errors.New("purpose must be one control-free line")
	}
	if strings.TrimSpace(candidate.ShortBody) == "" {
		return errors.New("short_body required")
	}
	if strings.ContainsRune(candidate.ShortBody, 0) {
		return errors.New("short_body contains NUL")
	}
	return nil
}

func structuredKnowledgeSafeLine(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func structuredKnowledgeModule(raw string) (knowledge.Module, error) {
	typeName := strings.ReplaceAll(strings.TrimSpace(strings.ToLower(raw)), "-", "_")
	switch typeName {
	case "memory", "runbook", "external_system", "related_project", "guideline":
		return knowledge.RouteByType(typeName)
	default:
		return nil, errors.New("type must be memory, runbook, external_system, related_project, or guideline")
	}
}

func StructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	projectID, ok := structuredKnowledgeProjectID(w, r)
	if !ok {
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		jsonError(w, "structured knowledge unavailable", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil || !sessionHomeProjectViewTx(r.Context(), tx, user, projectID) {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	snapshot, err := loadStructuredKnowledgeSnapshotTx(r.Context(), tx, projectID)
	if err != nil {
		jsonError(w, "structured knowledge unavailable", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "structured knowledge unavailable", http.StatusInternalServerError)
		return
	}
	jsonOK(w, snapshot)
}

func loadStructuredKnowledgeSnapshotTx(ctx context.Context, tx *sql.Tx, projectID int64) (models.StructuredKnowledgeSnapshot, error) {
	snapshot := models.StructuredKnowledgeSnapshot{
		SchemaVersion:       structuredKnowledgeSchemaVersion,
		ProjectID:           projectID,
		ShortBodyLimitBytes: structuredKnowledgeShortBodyLimitBytes,
		Entries:             []models.StructuredKnowledgeEntry{},
		Legacy:              []models.LegacyStructuredKnowledgeEntry{},
		Proposals:           []models.StructuredKnowledgeProposal{},
	}
	var compactID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT product_session_id FROM knowledge_compact_sessions WHERE project_id=?`, projectID).Scan(&compactID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if compactID.Valid {
		value := compactID.String
		snapshot.CompactProductSessionID = &value
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT sk.knowledge_id,sk.level,i.type,COALESCE(i.slug,''),i.title,sk.purpose,
		       COALESCE(i.description,''),sk.authored_product_session_id,sk.revision,
		       sk.created_at,sk.updated_at
		FROM structured_knowledge_entries sk
		JOIN issues i ON i.id=sk.knowledge_id
		WHERE sk.level='project' AND sk.origin_project_id=? AND i.project_id=? AND i.deleted_at IS NULL
		ORDER BY sk.updated_at DESC,sk.knowledge_id`, projectID, projectID)
	if err != nil {
		return snapshot, err
	}
	entries := []models.StructuredKnowledgeEntry{}
	for rows.Next() {
		var entry models.StructuredKnowledgeEntry
		if err := rows.Scan(&entry.KnowledgeID, &entry.Level, &entry.Type, &entry.Slug, &entry.Title,
			&entry.Purpose, &entry.ShortBody, &entry.AuthoredProductSessionID, &entry.Revision,
			&entry.CreatedAt, &entry.UpdatedAt); err != nil {
			rows.Close()
			return snapshot, err
		}
		entry.Links = []models.StructuredKnowledgeLink{}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, err
	}
	rows.Close()
	for i := range entries {
		entries[i].Validation, err = structuredKnowledgeValidationForTx(ctx, tx, projectID, entries[i].KnowledgeID,
			entries[i].Title, entries[i].ShortBody, structuredKnowledgeShortBodyLimitBytes)
		if err != nil {
			return snapshot, err
		}
	}
	snapshot.Entries = entries

	links, err := loadStructuredKnowledgeLinksTx(ctx, tx, snapshot.Entries)
	if err != nil {
		return snapshot, err
	}
	for i := range snapshot.Entries {
		snapshot.Entries[i].Links = links[snapshot.Entries[i].KnowledgeID]
		if snapshot.Entries[i].Links == nil {
			snapshot.Entries[i].Links = []models.StructuredKnowledgeLink{}
		}
	}

	legacyRows, err := tx.QueryContext(ctx, `
		SELECT i.id,i.type,COALESCE(i.slug,''),i.title,length(CAST(COALESCE(i.description,'') AS BLOB)),
		       COALESCE(i.description,''),strftime('%Y-%m-%dT%H:%M:%fZ',i.updated_at)
		FROM issues i LEFT JOIN structured_knowledge_entries sk ON sk.knowledge_id=i.id
		WHERE i.project_id=? AND i.deleted_at IS NULL AND i.slug IS NOT NULL AND sk.knowledge_id IS NULL
		  AND i.type IN ('memory','runbook','external_system','related_project','guideline')
		ORDER BY i.updated_at DESC,i.id`, projectID)
	if err != nil {
		return snapshot, err
	}
	type legacyCandidate struct {
		entry models.LegacyStructuredKnowledgeEntry
		body  string
	}
	legacyCandidates := []legacyCandidate{}
	for legacyRows.Next() {
		var entry models.LegacyStructuredKnowledgeEntry
		var body string
		if err := legacyRows.Scan(&entry.KnowledgeID, &entry.Type, &entry.Slug, &entry.Title, &entry.BodyBytes, &body, &entry.UpdatedAt); err != nil {
			legacyRows.Close()
			return snapshot, err
		}
		legacyCandidates = append(legacyCandidates, legacyCandidate{entry: entry, body: body})
	}
	if err := legacyRows.Err(); err != nil {
		legacyRows.Close()
		return snapshot, err
	}
	legacyRows.Close()
	for i := range legacyCandidates {
		legacyCandidates[i].entry.Validation, err = structuredKnowledgeValidationForTx(ctx, tx, projectID,
			legacyCandidates[i].entry.KnowledgeID, legacyCandidates[i].entry.Title,
			legacyCandidates[i].body, structuredKnowledgeShortBodyLimitBytes)
		if err != nil {
			return snapshot, err
		}
		legacyCandidates[i].entry.Validation.Flags = append(legacyCandidates[i].entry.Validation.Flags, "legacy_unstructured")
		sort.Strings(legacyCandidates[i].entry.Validation.Flags)
		snapshot.Legacy = append(snapshot.Legacy, legacyCandidates[i].entry)
	}

	proposalRows, err := tx.QueryContext(ctx, `
		SELECT proposal_id,source_kind,product_session_id,proposed_type,slug,title,purpose,candidate_body,
		       state,promoted_knowledge_id,created_at,updated_at
		FROM structured_knowledge_proposals WHERE project_id=?
		ORDER BY updated_at DESC,proposal_id`, projectID)
	if err != nil {
		return snapshot, err
	}
	proposals := []models.StructuredKnowledgeProposal{}
	for proposalRows.Next() {
		var proposal models.StructuredKnowledgeProposal
		if err := proposalRows.Scan(&proposal.ProposalID, &proposal.SourceKind, &proposal.ProductSessionID,
			&proposal.Type, &proposal.Slug, &proposal.Title, &proposal.Purpose, &proposal.CandidateBody,
			&proposal.State, &proposal.PromotedKnowledgeID, &proposal.CreatedAt, &proposal.UpdatedAt); err != nil {
			proposalRows.Close()
			return snapshot, err
		}
		proposals = append(proposals, proposal)
	}
	if err := proposalRows.Err(); err != nil {
		proposalRows.Close()
		return snapshot, err
	}
	proposalRows.Close()
	for i := range proposals {
		proposals[i].Validation, err = structuredKnowledgeValidationForTx(ctx, tx, projectID, 0,
			proposals[i].Title, proposals[i].CandidateBody, structuredKnowledgeShortBodyLimitBytes)
		if err != nil {
			return snapshot, err
		}
	}
	snapshot.Proposals = proposals
	return snapshot, nil
}

func structuredKnowledgeProblem(w http.ResponseWriter, err error, action string) {
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		jsonError(w, "structured knowledge identity already exists", http.StatusConflict)
		return
	}
	jsonError(w, action+" failed", http.StatusInternalServerError)
}

func ensureStructuredCompactTx(ctx context.Context, tx *sql.Tx, projectID int64) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT kc.product_session_id
		FROM knowledge_compact_sessions kc JOIN product_sessions ps ON ps.product_session_id=kc.product_session_id
		WHERE kc.project_id=? AND ps.project_id=kc.project_id AND ps.target_kind='paimos'
		AND ps.target_project_agent_id IS NULL`, projectID).Scan(&id)
	return id, err
}

func structuredKnowledgePaimosProductSessionTx(ctx context.Context, tx *sql.Tx, projectID int64, productSessionID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_sessions
		WHERE product_session_id=? AND project_id=? AND target_kind='paimos'
		AND target_project_agent_id IS NULL)`, productSessionID, projectID).Scan(&exists)
	return exists == 1, err
}

func knowledgeIssueType(typeName string) string {
	return strings.ReplaceAll(typeName, "-", "_")
}

func structuredKnowledgeCreateMutationTx(r *http.Request, tx *sql.Tx, issueID, actorUserID int64) error {
	after, err := fetchIssueMutationSnapshotTx(tx, issueID)
	if err != nil {
		return err
	}
	userID := actorUserID
	_, err = recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID:    requestIDFromRequest(r),
		UserID:       &userID,
		SessionID:    sessionIDFromRequest(r),
		AgentName:    agentNameFromRequest(r),
		MutationType: mutationTypeForRequest(r, "issue.create"),
		SubjectType:  "issue",
		SubjectID:    issueID,
		InverseOp:    InverseOp{Method: http.MethodDelete, Path: fmt.Sprintf("/issues/%d", issueID)},
		BeforeState:  nil,
		AfterState:   after,
		Undoable:     true,
	})
	return err
}
