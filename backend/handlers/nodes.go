// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const nodeSelect = `
	SELECT i.id,i.project_id,p.key||'-'||i.issue_number,i.type,parent.source_id,
	       i.title,i.description,i.status,i.priority,i.created_at,i.updated_at
	FROM issues i
	JOIN projects p ON p.id=i.project_id
	LEFT JOIN issue_relations parent ON parent.target_id=i.id AND parent.type='parent'`

type createNodeRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	ParentNodeID *int64 `json:"parent_node_id"`
}

type setNodeParentRequest struct {
	ParentNodeID json.RawMessage `json:"parent_node_id"`
}

func RegisterNodeRoutes(r chi.Router) {
	r.With(auth.RequireProjectView).Get("/projects/{id}/nodes", ListNodes)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/nodes", CreateNode)
	r.With(auth.RequireProjectView).Get("/projects/{id}/nodes/{nodeID}", GetNode)
	r.With(auth.RequireProjectEdit).Put("/projects/{id}/nodes/{nodeID}/parent", SetNodeParent)
}

func cosmeticNodeTypeLabel(issueType string) string {
	switch issueType {
	case "epic":
		return "Epic"
	case "task":
		return "Task"
	case "ticket":
		return "Ticket"
	case "cost_unit":
		return "Cost unit"
	case "release":
		return "Release"
	case "sprint":
		return "Sprint"
	case "memory":
		return "Memory"
	case "runbook":
		return "Runbook"
	case "external_system":
		return "External system"
	case "related_project":
		return "Related project"
	case "guideline":
		return "Guideline"
	default:
		return "Node"
	}
}

func scanNode(row interface{ Scan(...any) error }) (models.Node, error) {
	var out models.Node
	var legacyType string
	err := row.Scan(&out.NodeID, &out.ProjectID, &out.NodeKey, &legacyType, &out.ParentNodeID,
		&out.Title, &out.Description, &out.Status, &out.Priority, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	out.Kind = "node"
	out.CosmeticTypeLabel = cosmeticNodeTypeLabel(legacyType)
	return out, nil
}

func loadNode(projectID, nodeID int64) (models.Node, error) {
	return scanNode(db.DB.QueryRow(nodeSelect+
		` WHERE i.project_id=? AND i.id=? AND i.deleted_at IS NULL`, projectID, nodeID))
}

func ListNodes(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	rows, err := db.DB.QueryContext(r.Context(), nodeSelect+
		` WHERE i.project_id=? AND i.deleted_at IS NULL ORDER BY i.issue_number`, projectID)
	if err != nil {
		jsonError(w, "node list failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []models.Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			jsonError(w, "node list failed", http.StatusInternalServerError)
			return
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "node list failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func GetNode(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil || nodeID <= 0 {
		jsonError(w, "invalid node id", http.StatusBadRequest)
		return
	}
	out, err := loadNode(projectID, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "node not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "node read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func CreateNode(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	if !requireProjectAcceptsNewIssues(w, projectID) {
		return
	}
	var body createNodeRequest
	if !decodeProductSessionJSON(w, r, &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if body.Title == "" {
		jsonError(w, "title required", http.StatusBadRequest)
		return
	}
	if body.ParentNodeID != nil && *body.ParentNodeID <= 0 {
		jsonError(w, "parent_node_id must be positive", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "node create failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	number, err := db.NextIssueNumber(r.Context(), tx, projectID)
	if err != nil {
		jsonError(w, "node numbering failed", http.StatusInternalServerError)
		return
	}
	var createdBy *int64
	if principal, ok := auth.GetPrincipal(r); ok && principal.ActorUserID() > 0 {
		actor := principal.ActorUserID()
		createdBy = &actor
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by)
		VALUES(?,?, 'ticket',?,?,'new','medium',?)`, projectID, number, body.Title, body.Description, createdBy)
	if err != nil {
		jsonError(w, "node create failed", http.StatusInternalServerError)
		return
	}
	nodeID, _ := result.LastInsertId()
	if body.ParentNodeID != nil && *body.ParentNodeID == nodeID {
		jsonError(w, "node cannot parent itself", http.StatusUnprocessableEntity)
		return
	}
	if err := setParentEdge(r.Context(), tx, nodeID, body.ParentNodeID); err != nil {
		jsonError(w, "parent must be a same-project node allowed by project depth", http.StatusUnprocessableEntity)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "node create failed", http.StatusInternalServerError)
		return
	}
	issue := getIssueByID(nodeID)
	if issue != nil {
		saveSnapshot(issue, auth.GetUser(r), r)
	}
	out, err := loadNode(projectID, nodeID)
	if err != nil {
		jsonError(w, "node read failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, out)
}

func SetNodeParent(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil || nodeID <= 0 {
		jsonError(w, "invalid node id", http.StatusBadRequest)
		return
	}
	if _, err := loadNode(projectID, nodeID); errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "node not found", http.StatusNotFound)
		return
	} else if err != nil {
		jsonError(w, "node read failed", http.StatusInternalServerError)
		return
	}
	var body setNodeParentRequest
	if !decodeProductSessionJSON(w, r, &body) {
		return
	}
	if body.ParentNodeID == nil {
		jsonError(w, "parent_node_id is required", http.StatusBadRequest)
		return
	}
	var parentNodeID *int64
	if string(body.ParentNodeID) != "null" {
		var value int64
		if err := json.Unmarshal(body.ParentNodeID, &value); err != nil || value <= 0 {
			jsonError(w, "parent_node_id must be positive or null", http.StatusBadRequest)
			return
		}
		if value == nodeID {
			jsonError(w, "node cannot parent itself", http.StatusUnprocessableEntity)
			return
		}
		parentNodeID = &value
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "parent update failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := setParentEdge(r.Context(), tx, nodeID, parentNodeID); err != nil {
		jsonError(w, "parent must be a same-project node allowed by project depth without a cycle", http.StatusUnprocessableEntity)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "parent update failed", http.StatusInternalServerError)
		return
	}
	if issue := getIssueByID(nodeID); issue != nil {
		saveSnapshot(issue, auth.GetUser(r), r)
	}
	out, err := loadNode(projectID, nodeID)
	if err != nil {
		jsonError(w, "node read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}
