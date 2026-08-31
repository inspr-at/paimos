// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

type structuredKnowledgePromotionPolicyV1 struct {
	Enabled                      bool
	ProjectToInstanceAdmin       bool
	InstanceToTerminalSuperAdmin bool
}

// Production authority matrix: project knowledge can advance exactly one
// level to instance under an instance admin; instance knowledge can advance
// exactly one terminal step to kernel or vision under a super-admin. Direct
// project-to-terminal promotion is rejected by authorizeStructuredKnowledgePromotion.
var structuredKnowledgePromotionPolicy = structuredKnowledgePromotionPolicyV1{
	Enabled:                      true,
	ProjectToInstanceAdmin:       true,
	InstanceToTerminalSuperAdmin: true,
}

type promoteStructuredKnowledgeRequest struct {
	ToLevel string `json:"to_level"`
}

var (
	errStructuredPromotionNotFound   = errors.New("structured knowledge promotion source not found")
	errStructuredPromotionForbidden  = errors.New("structured knowledge promotion forbidden")
	errStructuredPromotionTransition = errors.New("structured knowledge promotion transition invalid")
	errStructuredPromotionCollision  = errors.New("structured knowledge promotion destination collision")
)

func PromoteStructuredKnowledgeV1(w http.ResponseWriter, r *http.Request) {
	structuredKnowledgeNoStore(w)
	knowledgeID, ok := structuredKnowledgeID(w, r)
	if !ok {
		return
	}
	var body promoteStructuredKnowledgeRequest
	if !decodeStructuredKnowledgeJSON(w, r, 4096, &body) {
		return
	}
	body.ToLevel = strings.ToLower(strings.TrimSpace(body.ToLevel))
	if !structuredKnowledgePromotionPolicy.Enabled {
		jsonError(w, "structured knowledge promotion authority is not configured", http.StatusServiceUnavailable)
		return
	}
	result, sourceProjectID, err := promoteStructuredKnowledgeTx(r.Context(), r, knowledgeID, body.ToLevel, structuredKnowledgePromotionPolicy)
	switch {
	case errors.Is(err, errStructuredPromotionNotFound):
		jsonError(w, "not found", http.StatusNotFound)
	case errors.Is(err, errStructuredPromotionForbidden):
		jsonError(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, errStructuredPromotionTransition):
		jsonError(w, "promotion must be project to instance or instance to kernel/vision", http.StatusUnprocessableEntity)
	case errors.Is(err, errStructuredPromotionCollision):
		jsonError(w, "knowledge identity already exists at the destination", http.StatusConflict)
	case err != nil:
		jsonError(w, "structured knowledge promotion failed", http.StatusInternalServerError)
	default:
		actor := &models.User{ID: result.ActorUserID}
		if issue := getIssueByID(result.Entry.KnowledgeID); issue != nil {
			saveSnapshot(issue, actor, r)
		}
		if source := getIssueByID(knowledgeID); source != nil {
			saveSnapshot(source, actor, r)
		}
		_ = sourceProjectID
		jsonOK(w, result)
	}
}

type promotionSource struct {
	KnowledgeID, OriginProjectID, Revision                    int64
	Level, Type, Slug, Title, Body, Purpose, ProductSessionID string
	Status, Priority, Metadata                                string
}

type promotionLink struct {
	LinkID, SourceID, TargetID                           int64
	Kind, SourceType, SourceSlug, TargetType, TargetSlug string
	SourceStructured, TargetStructured                   bool
}

func promoteStructuredKnowledgeTx(
	ctx context.Context,
	r *http.Request,
	knowledgeID int64,
	toLevel string,
	policy structuredKnowledgePromotionPolicyV1,
) (models.StructuredKnowledgePromotionResult, int64, error) {
	var response models.StructuredKnowledgePromotionResult
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return response, 0, err
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(ctx, tx, r, time.Now().UTC())
	if err != nil || user == nil {
		return response, 0, errStructuredPromotionNotFound
	}
	response.ActorUserID = user.ID

	var source promotionSource
	err = tx.QueryRowContext(ctx, `SELECT sk.knowledge_id,sk.level,sk.origin_project_id,sk.revision,
		i.type,COALESCE(i.slug,''),i.title,COALESCE(i.description,''),sk.purpose,sk.authored_product_session_id,
		i.status,i.priority,COALESCE(i.category_metadata,'')
		FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		WHERE sk.knowledge_id=? AND i.deleted_at IS NULL`, knowledgeID).Scan(
		&source.KnowledgeID, &source.Level, &source.OriginProjectID, &source.Revision,
		&source.Type, &source.Slug, &source.Title, &source.Body, &source.Purpose, &source.ProductSessionID,
		&source.Status, &source.Priority, &source.Metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return response, 0, errStructuredPromotionNotFound
	}
	if err != nil {
		return response, 0, err
	}

	projectSourceAuthorized := false
	if source.Level == "project" {
		if !sessionHomeProjectViewTx(ctx, tx, user, source.OriginProjectID) {
			return response, source.OriginProjectID, errStructuredPromotionNotFound
		}
		allowed, err := canEditProjectTx(ctx, tx, user, source.OriginProjectID)
		if err != nil {
			return response, source.OriginProjectID, err
		}
		if !allowed {
			return response, source.OriginProjectID, errStructuredPromotionNotFound
		}
		projectSourceAuthorized = true
	}
	if err := authorizeStructuredKnowledgePromotion(user, source.Level, toLevel, projectSourceAuthorized, policy); err != nil {
		return response, source.OriginProjectID, err
	}

	var collision int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issues
		WHERE project_id IS NULL AND user_id IS NULL AND type=? AND slug=? AND deleted_at IS NULL AND id<>?)`,
		source.Type, source.Slug, source.KnowledgeID).Scan(&collision)
	if err != nil {
		return response, source.OriginProjectID, err
	}
	if collision == 1 {
		return response, source.OriginProjectID, errStructuredPromotionCollision
	}

	links, err := loadPromotionLinksTx(ctx, tx, source.KnowledgeID)
	if err != nil {
		return response, source.OriginProjectID, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM structured_knowledge_links WHERE source_knowledge_id=? OR target_issue_id=?`, source.KnowledgeID, source.KnowledgeID); err != nil {
		return response, source.OriginProjectID, err
	}
	now := structuredKnowledgeNow()
	if _, err := tx.ExecContext(ctx, `UPDATE issues SET deleted_at=?,deleted_by=?,updated_at=? WHERE id=? AND deleted_at IS NULL`,
		now, user.ID, now, source.KnowledgeID); err != nil {
		return response, source.OriginProjectID, err
	}
	var nextNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(issue_number),0)+1 FROM issues WHERE project_id IS NULL AND user_id IS NULL`).Scan(&nextNumber); err != nil {
		return response, source.OriginProjectID, err
	}
	created, err := tx.ExecContext(ctx, `INSERT INTO issues(project_id,user_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(NULL,NULL,?,?,?,?,?,?,?,?,?,?)`, nextNumber, source.Type, source.Title, source.Body,
		source.Status, source.Priority, user.ID, source.Slug, source.Metadata, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return response, source.OriginProjectID, errStructuredPromotionCollision
		}
		return response, source.OriginProjectID, err
	}
	destinationID, _ := created.LastInsertId()
	promotionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO structured_knowledge_promotions(
		promotion_id,source_knowledge_id,destination_knowledge_id,from_level,to_level,actor_user_id,created_at)
		VALUES(?,?,?,?,?,?,?)`, promotionID, source.KnowledgeID, destinationID, source.Level, toLevel, user.ID, now); err != nil {
		return response, source.OriginProjectID, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,?,?,?,1,?,?,?,?)`, destinationID, toLevel, source.OriginProjectID, source.Purpose,
		source.ProductSessionID, user.ID, user.ID, now, now); err != nil {
		return response, source.OriginProjectID, err
	}

	response = models.StructuredKnowledgePromotionResult{
		PromotionID: promotionID, FromLevel: source.Level, ToLevel: toLevel,
		Links: []models.StructuredKnowledgePromotionLinkResult{}, ActorUserID: user.ID,
	}
	for _, link := range links {
		linkResult, err := remapPromotionLinkTx(ctx, tx, promotionID, source.KnowledgeID, destinationID, toLevel, user.ID, link)
		if err != nil {
			return response, source.OriginProjectID, err
		}
		response.Links = append(response.Links, linkResult)
	}
	if err := structuredKnowledgeCreateMutationTx(r, tx, destinationID, user.ID); err != nil {
		return response, source.OriginProjectID, err
	}
	if err := recordStructuredPromotionSourceDeleteTx(r, tx, source.KnowledgeID, user.ID); err != nil {
		return response, source.OriginProjectID, err
	}
	entry, err := loadStructuredKnowledgeEntryProjectionTx(ctx, tx, source.OriginProjectID, destinationID)
	if err != nil {
		return response, source.OriginProjectID, err
	}
	response.Entry = entry
	if err := tx.Commit(); err != nil {
		return response, source.OriginProjectID, err
	}
	return response, source.OriginProjectID, nil
}

func authorizeStructuredKnowledgePromotion(
	user *models.User,
	fromLevel, toLevel string,
	projectSourceAuthorized bool,
	policy structuredKnowledgePromotionPolicyV1,
) error {
	switch {
	case fromLevel == "project":
		if !projectSourceAuthorized {
			return errStructuredPromotionNotFound
		}
		if toLevel != "instance" {
			return errStructuredPromotionTransition
		}
		if !policy.ProjectToInstanceAdmin || !auth.IsAdmin(user) {
			return errStructuredPromotionForbidden
		}
		return nil
	case fromLevel == "instance":
		if toLevel != "kernel" && toLevel != "vision" {
			return errStructuredPromotionTransition
		}
		// Instance knowledge is a super-admin-only namespace. Conceal its
		// existence from every lower role instead of returning a role oracle.
		if !policy.InstanceToTerminalSuperAdmin || !auth.IsSuperAdmin(user) {
			return errStructuredPromotionNotFound
		}
		return nil
	default:
		return errStructuredPromotionTransition
	}
}

func loadPromotionLinksTx(ctx context.Context, tx *sql.Tx, knowledgeID int64) ([]promotionLink, error) {
	rows, err := tx.QueryContext(ctx, `SELECT l.link_id,l.source_knowledge_id,l.target_issue_id,l.canonical_kind,
		si.type,COALESCE(si.slug,''),CASE WHEN ssk.knowledge_id IS NULL THEN 0 ELSE 1 END,
		ti.type,COALESCE(ti.slug,''),CASE WHEN tsk.knowledge_id IS NULL THEN 0 ELSE 1 END
		FROM structured_knowledge_links l
		JOIN issues si ON si.id=l.source_knowledge_id AND si.deleted_at IS NULL
		JOIN issues ti ON ti.id=l.target_issue_id AND ti.deleted_at IS NULL
		LEFT JOIN structured_knowledge_entries ssk ON ssk.knowledge_id=si.id
		LEFT JOIN structured_knowledge_entries tsk ON tsk.knowledge_id=ti.id
		WHERE l.source_knowledge_id=? OR l.target_issue_id=? ORDER BY l.link_id`, knowledgeID, knowledgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []promotionLink{}
	for rows.Next() {
		var link promotionLink
		var sourceStructured, targetStructured int
		if err := rows.Scan(&link.LinkID, &link.SourceID, &link.TargetID, &link.Kind,
			&link.SourceType, &link.SourceSlug, &sourceStructured,
			&link.TargetType, &link.TargetSlug, &targetStructured); err != nil {
			return nil, err
		}
		link.SourceStructured = sourceStructured == 1
		link.TargetStructured = targetStructured == 1
		out = append(out, link)
	}
	return out, rows.Err()
}

func destinationKnowledgeIdentityTx(ctx context.Context, tx *sql.Tx, level, typeName, slug string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT sk.knowledge_id FROM structured_knowledge_entries sk
		JOIN issues i ON i.id=sk.knowledge_id
		WHERE sk.level=? AND i.project_id IS NULL AND i.user_id IS NULL AND i.type=? AND i.slug=? AND i.deleted_at IS NULL`,
		level, typeName, slug).Scan(&id)
	return id, err
}

func remapPromotionLinkTx(
	ctx context.Context,
	tx *sql.Tx,
	promotionID string,
	sourceKnowledgeID, destinationKnowledgeID int64,
	toLevel string,
	actorUserID int64,
	link promotionLink,
) (models.StructuredKnowledgePromotionLinkResult, error) {
	result := models.StructuredKnowledgePromotionLinkResult{OriginalLinkID: link.LinkID, Outcome: "dropped"}
	newSource, newTarget := link.SourceID, link.TargetID
	var otherType, otherSlug string
	var otherStructured bool
	incoming := link.TargetID == sourceKnowledgeID
	if link.SourceID == sourceKnowledgeID {
		newSource = destinationKnowledgeID
		otherType, otherSlug, otherStructured = link.TargetType, link.TargetSlug, link.TargetStructured
	} else {
		newTarget = destinationKnowledgeID
		otherType, otherSlug, otherStructured = link.SourceType, link.SourceSlug, link.SourceStructured
	}
	if !otherStructured {
		result.Reason = "node_target_dropped"
		return recordPromotionLinkResultTx(ctx, tx, promotionID, result)
	}
	mappedOther, err := destinationKnowledgeIdentityTx(ctx, tx, toLevel, otherType, otherSlug)
	if errors.Is(err, sql.ErrNoRows) {
		if incoming {
			result.Reason = "incoming_scope_edge_dropped"
		} else {
			result.Reason = "target_missing_at_destination"
		}
		return recordPromotionLinkResultTx(ctx, tx, promotionID, result)
	}
	if err != nil {
		return result, err
	}
	if link.SourceID == sourceKnowledgeID {
		newTarget = mappedOther
	} else {
		newSource = mappedOther
	}
	if link.Kind == "see_also" && newSource > newTarget {
		newSource, newTarget = newTarget, newSource
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO structured_knowledge_links(
		source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id) VALUES(?,?,?,?)`,
		newSource, newTarget, link.Kind, actorUserID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "parent") {
			result.Reason = "destination_graph_conflict"
			return recordPromotionLinkResultTx(ctx, tx, promotionID, result)
		}
		return result, err
	}
	resultingID, _ := inserted.LastInsertId()
	result.Outcome = "remapped"
	result.Reason = "same_scope_identity"
	result.ResultingLinkID = &resultingID
	return recordPromotionLinkResultTx(ctx, tx, promotionID, result)
}

func recordPromotionLinkResultTx(
	ctx context.Context,
	tx *sql.Tx,
	promotionID string,
	result models.StructuredKnowledgePromotionLinkResult,
) (models.StructuredKnowledgePromotionLinkResult, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO structured_knowledge_promotion_links(
		promotion_id,original_link_id,outcome,resulting_link_id,reason) VALUES(?,?,?,?,?)`,
		promotionID, result.OriginalLinkID, result.Outcome, result.ResultingLinkID, result.Reason)
	return result, err
}

func recordStructuredPromotionSourceDeleteTx(r *http.Request, tx *sql.Tx, sourceID, actorUserID int64) error {
	after, err := fetchIssueMutationSnapshotTx(tx, sourceID)
	if err != nil {
		return err
	}
	before := after
	before.DeletedAt = nil
	userID := actorUserID
	_, err = recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID: requestIDFromRequest(r), UserID: &userID, SessionID: sessionIDFromRequest(r),
		AgentName: agentNameFromRequest(r), MutationType: mutationTypeForRequest(r, "issue.delete"),
		SubjectType: "issue", SubjectID: sourceID,
		InverseOp:   InverseOp{},
		BeforeState: before, AfterState: after, Undoable: false,
	})
	return err
}
