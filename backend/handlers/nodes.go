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
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const nodeSelect = `
	SELECT i.id,i.project_id,p.key||'-'||i.issue_number,i.type,
	       COALESCE(label_override.label,label_default.label,'Node'),parent.source_id,
	       i.title,i.description,i.status,i.priority,i.created_at,i.updated_at
	FROM issues i
	JOIN projects p ON p.id=i.project_id
	LEFT JOIN node_label_defaults label_default ON label_default.issue_type=i.type
	LEFT JOIN project_node_label_overrides label_override
	 ON label_override.project_id=i.project_id AND label_override.issue_type=i.type
	LEFT JOIN issue_relations parent ON parent.target_id=i.id AND parent.type='parent'`

var nodeLabelTypes = []string{"epic", "task", "ticket", "cost_unit", "release", "sprint", "memory", "runbook", "external_system", "related_project", "guideline"}

type createNodeRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	ParentNodeID *int64 `json:"parent_node_id"`
}

type setNodeParentRequest struct {
	ParentNodeID json.RawMessage `json:"parent_node_id"`
}

func RegisterNodeRoutes(r chi.Router) {
	r.Get("/node-labels", GetGlobalNodeLabels)
	r.With(auth.RequireAdmin).Put("/node-labels", PutGlobalNodeLabels)
	r.With(auth.RequireProjectView).Get("/projects/{id}/node-labels", GetProjectNodeLabels)
	r.With(auth.RequireProjectEdit).Put("/projects/{id}/node-labels", PutProjectNodeLabels)
	r.With(auth.RequireProjectView).Get("/projects/{id}/nodes", ListNodes)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/nodes", CreateNode)
	r.With(auth.RequireProjectView).Get("/projects/{id}/nodes/{nodeID}", GetNode)
	r.With(auth.RequireProjectEdit).Put("/projects/{id}/nodes/{nodeID}/parent", SetNodeParent)
}

func scanNode(row interface{ Scan(...any) error }) (models.Node, error) {
	var out models.Node
	var legacyType, resolvedLabel string
	err := row.Scan(&out.NodeID, &out.ProjectID, &out.NodeKey, &legacyType, &resolvedLabel, &out.ParentNodeID,
		&out.Title, &out.Description, &out.Status, &out.Priority, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	out.Kind = "node"
	out.CosmeticTypeLabel = resolvedLabel
	return out, nil
}

func validNodeLabels(labels map[string]string, complete bool) bool {
	if complete && len(labels) != len(nodeLabelTypes) {
		return false
	}
	allowed := make(map[string]bool, len(nodeLabelTypes))
	for _, issueType := range nodeLabelTypes {
		allowed[issueType] = true
	}
	for issueType, label := range labels {
		if !allowed[issueType] || label != strings.TrimSpace(label) || len([]byte(label)) == 0 || len([]byte(label)) > 64 {
			return false
		}
		for _, value := range label {
			if unicode.IsControl(value) {
				return false
			}
		}
	}
	return true
}

func loadNodeLabelConfig(projectID *int64) (models.NodeLabelConfig, error) {
	out := models.NodeLabelConfig{Precedence: "project_override_then_global_default", GlobalDefaults: map[string]string{}, ProjectOverrides: map[string]string{}, Resolved: map[string]string{}}
	rows, err := db.DB.Query(`SELECT issue_type,label FROM node_label_defaults ORDER BY issue_type`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var issueType, label string
		if err := rows.Scan(&issueType, &label); err != nil {
			rows.Close()
			return out, err
		}
		out.GlobalDefaults[issueType] = label
		out.Resolved[issueType] = label
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if projectID == nil {
		return out, nil
	}
	rows, err = db.DB.Query(`SELECT issue_type,label FROM project_node_label_overrides WHERE project_id=? ORDER BY issue_type`, *projectID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var issueType, label string
		if err := rows.Scan(&issueType, &label); err != nil {
			return out, err
		}
		out.ProjectOverrides[issueType] = label
		out.Resolved[issueType] = label
	}
	return out, rows.Err()
}

func GetGlobalNodeLabels(w http.ResponseWriter, r *http.Request) {
	out, err := loadNodeLabelConfig(nil)
	if err != nil {
		jsonError(w, "node labels read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func PutGlobalNodeLabels(w http.ResponseWriter, r *http.Request) {
	var labels map[string]string
	if !decodeProductSessionJSON(w, r, &labels) {
		return
	}
	if !validNodeLabels(labels, true) {
		jsonError(w, "global node labels require every supported issue type and labels of 1-64 bytes without control characters", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "node labels update failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	for _, issueType := range nodeLabelTypes {
		if _, err := tx.ExecContext(r.Context(), `UPDATE node_label_defaults SET label=? WHERE issue_type=?`, labels[issueType], issueType); err != nil {
			jsonError(w, "node labels update failed", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "node labels update failed", http.StatusInternalServerError)
		return
	}
	GetGlobalNodeLabels(w, r)
}

func GetProjectNodeLabels(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	out, err := loadNodeLabelConfig(&projectID)
	if err != nil {
		jsonError(w, "node labels read failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, out)
}

func PutProjectNodeLabels(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	var labels map[string]string
	if !decodeProductSessionJSON(w, r, &labels) {
		return
	}
	if !validNodeLabels(labels, false) {
		jsonError(w, "project node labels must use supported issue types and labels of 1-64 bytes without control characters", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "node labels update failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM project_node_label_overrides WHERE project_id=?`, projectID); err != nil {
		jsonError(w, "node labels update failed", http.StatusInternalServerError)
		return
	}
	for issueType, label := range labels {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO project_node_label_overrides(project_id,issue_type,label) VALUES(?,?,?)`, projectID, issueType, label); err != nil {
			jsonError(w, "node labels update failed", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "node labels update failed", http.StatusInternalServerError)
		return
	}
	GetProjectNodeLabels(w, r)
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
