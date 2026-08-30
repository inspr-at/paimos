// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const productSessionSelect = `
	SELECT ps.product_session_id,ps.project_id,ps.target_kind,ps.target_project_agent_id,
	       CASE WHEN ps.target_kind='paimos' THEN 'paimos' ELSE COALESCE(pa.name,'') END,
	       ps.node_id,i.id,COALESCE(p.key||'-'||i.issue_number,''),COALESCE(i.title,''),
	       ps.title,ps.summary,ps.revision,ps.created_by_user_id,ps.updated_by_user_id,
	       ps.created_at,ps.updated_at
	FROM product_sessions ps
	LEFT JOIN project_agents pa ON pa.id=ps.target_project_agent_id AND pa.project_id=ps.project_id
	LEFT JOIN issues i ON i.id=ps.node_id AND i.project_id=ps.project_id AND i.deleted_at IS NULL
	LEFT JOIN projects p ON p.id=ps.project_id`

type createProductSessionRequest struct {
	TargetKind           string `json:"target_kind"`
	TargetProjectAgentID *int64 `json:"target_project_agent_id"`
	NodeID               *int64 `json:"node_id"`
	Title                string `json:"title"`
	Summary              string `json:"summary"`
}

type attachProductSessionNodeRequest struct {
	NodeID           int64 `json:"node_id"`
	ExpectedRevision int64 `json:"expected_revision"`
}

type detachProductSessionNodeRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func RegisterProductSessionRoutes(r chi.Router) {
	r.With(auth.AgentModePrivateNoStore, auth.RequireProjectView).Get("/projects/{id}/session-home/v1", SessionHomeV1)
	r.With(auth.RequireProjectView).Get("/projects/{id}/product-sessions", ListProductSessions)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/product-sessions", CreateProductSession)
	r.With(auth.RequireProjectView).Get("/projects/{id}/product-sessions/{productSessionID}", GetProductSession)
	r.With(auth.RequireProjectView).Get("/projects/{id}/product-sessions/{productSessionID}/events", ListProductSessionEvents)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/product-sessions/{productSessionID}/attach-node", AttachProductSessionNode)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/product-sessions/{productSessionID}/detach-node", DetachProductSessionNode)
}

func ListProductSessionEvents(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	productSessionID := chi.URLParam(r, "productSessionID")
	var exists int
	if err := db.DB.QueryRowContext(r.Context(), `SELECT 1 FROM product_sessions WHERE project_id=? AND product_session_id=?`, projectID, productSessionID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "product session not found", http.StatusNotFound)
		return
	} else if err != nil {
		jsonError(w, "product session events read failed", http.StatusInternalServerError)
		return
	}
	rows, err := db.DB.QueryContext(r.Context(), `SELECT event_id,product_session_id,event_sequence,operation,
		actor_user_id,before_node_id,after_node_id,before_revision,after_revision,created_at
		FROM product_session_events WHERE product_session_id=? ORDER BY event_sequence`, productSessionID)
	if err != nil {
		jsonError(w, "product session events read failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []models.ProductSessionEvent{}
	for rows.Next() {
		var event models.ProductSessionEvent
		if err := rows.Scan(&event.EventID, &event.ProductSessionID, &event.EventSequence, &event.Operation,
			&event.ActorUserID, &event.BeforeNodeID, &event.AfterNodeID, &event.BeforeRevision,
			&event.AfterRevision, &event.CreatedAt); err != nil {
			jsonError(w, "product session events read failed", http.StatusInternalServerError)
			return
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "product session events read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func decodeProductSessionJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	reader := http.MaxBytesReader(w, r.Body, 16*1024)
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		jsonError(w, "invalid product session body", http.StatusBadRequest)
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		jsonError(w, "invalid product session body", http.StatusBadRequest)
		return false
	}
	return true
}

func productSessionProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func scanProductSession(row interface{ Scan(...any) error }) (models.ProductSession, error) {
	var out models.ProductSession
	var nodeRowID sql.NullInt64
	var nodeKey, nodeTitle string
	err := row.Scan(&out.ProductSessionID, &out.ProjectID, &out.TargetKind, &out.TargetProjectAgentID,
		&out.TargetAgentName, &out.NodeID, &nodeRowID, &nodeKey, &nodeTitle,
		&out.Title, &out.Summary, &out.Revision, &out.CreatedByUserID, &out.UpdatedByUserID,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	if nodeRowID.Valid {
		out.Node = &models.ProductSessionNode{
			NodeID: nodeRowID.Int64, NodeKey: nodeKey, Label: nodeKey + " · " + nodeTitle,
		}
	}
	return out, nil
}

func loadProductSession(projectID int64, productSessionID string) (models.ProductSession, error) {
	return scanProductSession(db.DB.QueryRow(productSessionSelect+
		` WHERE ps.project_id=? AND ps.product_session_id=?`, projectID, productSessionID))
}

func CreateProductSession(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	principal, ok := auth.GetPrincipal(r)
	if !ok || principal.ActorUserID() <= 0 {
		jsonError(w, "authenticated actor required", http.StatusUnauthorized)
		return
	}
	var body createProductSessionRequest
	if !decodeProductSessionJSON(w, r, &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if body.Title == "" || len([]byte(body.Title)) > 240 || len([]byte(body.Summary)) > 4000 {
		jsonError(w, "title is required and product session text is too long", http.StatusBadRequest)
		return
	}
	if body.TargetKind != "paimos" && body.TargetKind != "project_agent" {
		jsonError(w, "target_kind must be paimos or project_agent", http.StatusBadRequest)
		return
	}
	if (body.TargetKind == "paimos" && body.TargetProjectAgentID != nil) ||
		(body.TargetKind == "project_agent" && (body.TargetProjectAgentID == nil || *body.TargetProjectAgentID <= 0)) {
		jsonError(w, "target_project_agent_id does not match target_kind", http.StatusBadRequest)
		return
	}
	if body.NodeID != nil && *body.NodeID <= 0 {
		jsonError(w, "invalid node_id", http.StatusBadRequest)
		return
	}

	productSessionID := uuid.NewString()
	actorUserID := principal.ActorUserID()
	_, err := db.DB.ExecContext(r.Context(), `INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,node_id,title,summary,
		created_by_user_id,updated_by_user_id)
		VALUES(?,?,?,?,?,?,?,?,?)`, productSessionID, projectID, body.TargetKind,
		body.TargetProjectAgentID, body.NodeID, body.Title, body.Summary, actorUserID, actorUserID)
	if err != nil {
		if strings.Contains(err.Error(), "project mismatch") || strings.Contains(err.Error(), "FOREIGN KEY") {
			jsonError(w, "target agent and node must belong to this project", http.StatusUnprocessableEntity)
			return
		}
		jsonError(w, "product session create failed", http.StatusInternalServerError)
		return
	}
	out, err := loadProductSession(projectID, productSessionID)
	if err != nil {
		jsonError(w, "product session read failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, out)
}

func ListProductSessions(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	rows, err := db.DB.QueryContext(r.Context(), productSessionSelect+
		` WHERE ps.project_id=? ORDER BY ps.updated_at DESC,ps.product_session_id`, projectID)
	if err != nil {
		jsonError(w, "product session list failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []models.ProductSession{}
	for rows.Next() {
		item, err := scanProductSession(rows)
		if err != nil {
			jsonError(w, "product session list failed", http.StatusInternalServerError)
			return
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "product session list failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func GetProductSession(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	out, err := loadProductSession(projectID, chi.URLParam(r, "productSessionID"))
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "product session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "product session read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func updateProductSessionNode(w http.ResponseWriter, r *http.Request, nodeID *int64, expectedRevision int64) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	principal, ok := auth.GetPrincipal(r)
	if !ok || principal.ActorUserID() <= 0 {
		jsonError(w, "authenticated actor required", http.StatusUnauthorized)
		return
	}
	if expectedRevision <= 0 {
		jsonError(w, "expected_revision must be positive", http.StatusBadRequest)
		return
	}
	productSessionID := chi.URLParam(r, "productSessionID")
	result, err := db.DB.ExecContext(r.Context(), `UPDATE product_sessions
		SET node_id=?,revision=revision+1,updated_by_user_id=?,
		    updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE product_session_id=? AND project_id=? AND revision=?`,
		nodeID, principal.ActorUserID(), productSessionID, projectID, expectedRevision)
	if err != nil {
		if strings.Contains(err.Error(), "node project mismatch") || strings.Contains(err.Error(), "FOREIGN KEY") {
			jsonError(w, "node must be a live node in this project", http.StatusUnprocessableEntity)
			return
		}
		jsonError(w, "product session attachment failed", http.StatusInternalServerError)
		return
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		var exists int
		err := db.DB.QueryRowContext(r.Context(), `SELECT 1 FROM product_sessions
			WHERE product_session_id=? AND project_id=?`, productSessionID, projectID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "product session not found", http.StatusNotFound)
		} else {
			jsonError(w, "product session revision conflict", http.StatusConflict)
		}
		return
	}
	out, err := loadProductSession(projectID, productSessionID)
	if err != nil {
		jsonError(w, "product session read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func AttachProductSessionNode(w http.ResponseWriter, r *http.Request) {
	var body attachProductSessionNodeRequest
	if !decodeProductSessionJSON(w, r, &body) {
		return
	}
	if body.NodeID <= 0 {
		jsonError(w, "node_id must be positive", http.StatusBadRequest)
		return
	}
	updateProductSessionNode(w, r, &body.NodeID, body.ExpectedRevision)
}

func DetachProductSessionNode(w http.ResponseWriter, r *http.Request) {
	var body detachProductSessionNodeRequest
	if !decodeProductSessionJSON(w, r, &body) {
		return
	}
	updateProductSessionNode(w, r, nil, body.ExpectedRevision)
}
