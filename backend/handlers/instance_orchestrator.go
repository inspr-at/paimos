// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	orchestratorSchemaVersion = 1
	orchestratorBodyLimit     = 16 * 1024
	orchestratorLabelMaxBytes = 64
	orchestratorRequestIDMax  = 128
)

type orchestratorState struct {
	Revision  int64
	UpdatedAt *string
	Target    *models.OrchestratorTarget
}

type orchestratorPutBody struct {
	ExpectedRevision *json.Number    `json:"expected_revision"`
	Orchestrator     json.RawMessage `json:"orchestrator"`
}

type orchestratorPutTarget struct {
	ProjectID    *json.Number `json:"project_id"`
	Key          *string      `json:"key"`
	DisplayLabel *string      `json:"display_label"`
}

func RegisterOrchestratorRoutes(r chi.Router) {
	r.Get("/orchestrator/v1", GetOrchestratorProjection)
	r.With(auth.RequireSuperAdmin).Get("/orchestrator/v1/config", GetOrchestratorConfig)
	r.With(auth.RequireSuperAdmin).Put("/orchestrator/v1/config", PutOrchestratorConfig)
	r.With(auth.RequireSuperAdmin).Get("/orchestrator/v1/events", ListOrchestratorEvents)
	r.Handle("/orchestrator/v1/*", http.HandlerFunc(http.NotFound))
}

func GetOrchestratorProjection(w http.ResponseWriter, r *http.Request) {
	state, ok := readAuthorizedOrchestrator(w, r, false)
	if !ok {
		return
	}
	var projection *models.OrchestratorProjection
	if state.Target != nil {
		projection = &models.OrchestratorProjection{DisplayLabel: state.Target.DisplayLabel}
	}
	writeOrchestratorJSON(w, http.StatusOK, models.OrchestratorProjectionResponse{
		SchemaVersion: orchestratorSchemaVersion,
		Revision:      state.Revision,
		Orchestrator:  projection,
		UpdatedAt:     state.UpdatedAt,
	})
}

func GetOrchestratorConfig(w http.ResponseWriter, r *http.Request) {
	state, ok := readAuthorizedOrchestrator(w, r, true)
	if !ok {
		return
	}
	writeOrchestratorJSON(w, http.StatusOK, orchestratorConfigResponse(state))
}

func readAuthorizedOrchestrator(w http.ResponseWriter, r *http.Request, superAdmin bool) (orchestratorState, bool) {
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		orchestratorInternalError(w, err)
		return orchestratorState{}, false
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeOrchestratorError(w, http.StatusUnauthorized, "unauthorized")
		return orchestratorState{}, false
	}
	if (superAdmin && !auth.IsSuperAdmin(user)) || (!superAdmin && !auth.IsInternalRole(user.Role)) {
		writeOrchestratorError(w, http.StatusForbidden, "forbidden")
		return orchestratorState{}, false
	}
	state, err := loadOrchestratorState(r.Context(), tx)
	if err != nil {
		orchestratorInternalError(w, err)
		return orchestratorState{}, false
	}
	if err := tx.Commit(); err != nil {
		orchestratorInternalError(w, err)
		return orchestratorState{}, false
	}
	return state, true
}

func PutOrchestratorConfig(w http.ResponseWriter, r *http.Request) {
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		orchestratorInternalError(w, err)
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeOrchestratorError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !auth.IsSuperAdmin(user) {
		writeOrchestratorError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body orchestratorPutBody
	if err := DecodeControlJSON(w, r, orchestratorBodyLimit, &body); err != nil {
		writeOrchestratorError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expectedRevision, ok := strictNonNegativeNumber(body.ExpectedRevision)
	if !ok {
		writeOrchestratorError(w, http.StatusBadRequest, "invalid_revision")
		return
	}
	requested, clear, invalidCode := decodeOrchestratorPutTarget(body.Orchestrator)
	if invalidCode != "" {
		writeOrchestratorError(w, http.StatusBadRequest, invalidCode)
		return
	}

	before, err := loadOrchestratorState(r.Context(), tx)
	if err != nil {
		orchestratorInternalError(w, err)
		return
	}
	if before.Revision != expectedRevision {
		writeOrchestratorError(w, http.StatusConflict, "revision_conflict")
		return
	}

	var afterTarget *models.OrchestratorTarget
	if !clear {
		afterTarget, err = resolveOrchestratorTarget(r.Context(), tx, requested)
		if errors.Is(err, sql.ErrNoRows) {
			writeOrchestratorError(w, http.StatusNotFound, "orchestrator_target_not_found")
			return
		}
		if err != nil {
			orchestratorInternalError(w, err)
			return
		}
	}
	operation, ok := orchestratorOperation(before.Target, afterTarget)
	if !ok {
		writeOrchestratorError(w, http.StatusBadRequest, "no_change")
		return
	}
	now := canonicalOrchestratorTimestamp(time.Now().UTC())
	afterRevision := before.Revision + 1
	var afterAgentID any
	var afterLabel any
	if afterTarget != nil {
		afterAgentID = afterTarget.ProjectAgentID
		afterLabel = afterTarget.DisplayLabel
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE instance_orchestrator
		SET project_agent_id=?,display_label=?,revision=?,updated_by_user_id=?,updated_at=?
		WHERE singleton_id=1 AND revision=?`, afterAgentID, afterLabel, afterRevision, user.ID, now, before.Revision)
	if err != nil {
		orchestratorInternalError(w, err)
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		orchestratorInternalError(w, err)
		return
	}
	if rows != 1 {
		writeOrchestratorError(w, http.StatusConflict, "revision_conflict")
		return
	}
	if err := insertOrchestratorEvent(r.Context(), tx, operation, user.ID, orchestratorRequestID(r), before.Revision, afterRevision, before.Target, afterTarget, now); err != nil {
		orchestratorInternalError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		orchestratorInternalError(w, err)
		return
	}
	after := orchestratorState{Revision: afterRevision, UpdatedAt: &now, Target: afterTarget}
	writeOrchestratorJSON(w, http.StatusOK, orchestratorConfigResponse(after))
}

func ListOrchestratorEvents(w http.ResponseWriter, r *http.Request) {
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		orchestratorInternalError(w, err)
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeOrchestratorError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !auth.IsSuperAdmin(user) {
		writeOrchestratorError(w, http.StatusForbidden, "forbidden")
		return
	}
	afterRevision, limit, ok := parseOrchestratorEventQuery(r.URL)
	if !ok {
		writeOrchestratorError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT event_id,operation,actor_user_id,request_id,before_revision,after_revision,
		before_project_agent_id,before_project_id,before_key,before_display_label,
		after_project_agent_id,after_project_id,after_key,after_display_label,created_at
		FROM instance_orchestrator_events WHERE after_revision>? ORDER BY after_revision ASC LIMIT ?`, afterRevision, limit)
	if err != nil {
		orchestratorInternalError(w, err)
		return
	}
	events := []models.OrchestratorEvent{}
	for rows.Next() {
		event, err := scanOrchestratorEvent(rows)
		if err != nil {
			rows.Close()
			orchestratorInternalError(w, err)
			return
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		orchestratorInternalError(w, err)
		return
	}
	if err := rows.Err(); err != nil {
		orchestratorInternalError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		orchestratorInternalError(w, err)
		return
	}
	var next *int64
	if len(events) > 0 {
		value := events[len(events)-1].AfterRevision
		next = &value
	}
	writeOrchestratorJSON(w, http.StatusOK, models.OrchestratorEventsResponse{
		SchemaVersion: orchestratorSchemaVersion, Events: events, NextAfterRevision: next,
	})
}

func loadOrchestratorState(ctx context.Context, tx *sql.Tx) (orchestratorState, error) {
	var state orchestratorState
	var agentID, projectID sql.NullInt64
	var displayLabel, updatedAt, projectKey, agentKey sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT io.revision,io.updated_at,io.project_agent_id,io.display_label,
		pa.project_id,p.key,pa.name
		FROM instance_orchestrator io
		LEFT JOIN project_agents pa ON pa.id=io.project_agent_id
		LEFT JOIN projects p ON p.id=pa.project_id
		WHERE io.singleton_id=1`).Scan(&state.Revision, &updatedAt, &agentID, &displayLabel, &projectID, &projectKey, &agentKey)
	if err != nil {
		return state, err
	}
	if updatedAt.Valid {
		state.UpdatedAt = &updatedAt.String
	}
	if agentID.Valid {
		if !projectID.Valid || !displayLabel.Valid || !projectKey.Valid || !agentKey.Valid {
			return state, errors.New("orchestrator target invariant failed")
		}
		state.Target = &models.OrchestratorTarget{ProjectID: projectID.Int64, ProjectKey: projectKey.String,
			ProjectAgentID: agentID.Int64, Key: agentKey.String, DisplayLabel: displayLabel.String}
	}
	return state, nil
}

func resolveOrchestratorTarget(ctx context.Context, tx *sql.Tx, requested models.OrchestratorTarget) (*models.OrchestratorTarget, error) {
	target := &models.OrchestratorTarget{ProjectID: requested.ProjectID, Key: requested.Key, DisplayLabel: requested.DisplayLabel}
	err := tx.QueryRowContext(ctx, `SELECT pa.id,p.key FROM project_agents pa JOIN projects p ON p.id=pa.project_id
		WHERE pa.project_id=? AND pa.name=? AND p.status<>'deleted'`, requested.ProjectID, requested.Key).
		Scan(&target.ProjectAgentID, &target.ProjectKey)
	return target, err
}

func decodeOrchestratorPutTarget(raw json.RawMessage) (models.OrchestratorTarget, bool, string) {
	if len(raw) == 0 {
		return models.OrchestratorTarget{}, false, "invalid_orchestrator"
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return models.OrchestratorTarget{}, true, ""
	}
	var input orchestratorPutTarget
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return models.OrchestratorTarget{}, false, "invalid_request"
	}
	if err := decoder.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return models.OrchestratorTarget{}, false, "invalid_request"
	}
	projectID, ok := strictPositiveNumber(input.ProjectID)
	if !ok || input.Key == nil || input.DisplayLabel == nil || !validOrchestratorKey(*input.Key) || !validOrchestratorLabel(*input.DisplayLabel) {
		return models.OrchestratorTarget{}, false, "invalid_orchestrator"
	}
	return models.OrchestratorTarget{ProjectID: projectID, Key: *input.Key, DisplayLabel: *input.DisplayLabel}, false, ""
}

func strictNonNegativeNumber(number *json.Number) (int64, bool) {
	if number == nil {
		return 0, false
	}
	raw := number.String()
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value >= 0 && strconv.FormatInt(value, 10) == raw
}

func strictPositiveNumber(number *json.Number) (int64, bool) {
	value, ok := strictNonNegativeNumber(number)
	return value, ok && value > 0
}

func validOrchestratorKey(key string) bool {
	return len(key) <= agentNameMaxLen && agentNamePattern.MatchString(key) && !reservedAgentNames[key]
}

func validOrchestratorLabel(label string) bool {
	if !utf8.ValidString(label) || len(label) == 0 || len(label) > orchestratorLabelMaxBytes || strings.TrimSpace(label) != label {
		return false
	}
	for _, r := range label {
		if r <= 31 || r == 127 {
			return false
		}
	}
	return true
}

func orchestratorOperation(before, after *models.OrchestratorTarget) (string, bool) {
	switch {
	case before == nil && after == nil:
		return "", false
	case before == nil:
		return "set", true
	case after == nil:
		return "clear", true
	case before.ProjectAgentID != after.ProjectAgentID:
		return "reassign", true
	case before.DisplayLabel != after.DisplayLabel:
		return "display_label_update", true
	default:
		return "", false
	}
}

func insertOrchestratorEvent(ctx context.Context, tx *sql.Tx, operation string, actorUserID int64, requestID string,
	beforeRevision, afterRevision int64, before, after *models.OrchestratorTarget, createdAt string) error {
	values := func(target *models.OrchestratorTarget) (any, any, any, any) {
		if target == nil {
			return nil, nil, nil, nil
		}
		return target.ProjectAgentID, target.ProjectID, target.Key, target.DisplayLabel
	}
	beforeAgent, beforeProject, beforeKey, beforeLabel := values(before)
	afterAgent, afterProject, afterKey, afterLabel := values(after)
	_, err := tx.ExecContext(ctx, `INSERT INTO instance_orchestrator_events(operation,actor_user_id,request_id,
		before_revision,after_revision,before_project_agent_id,before_project_id,before_key,before_display_label,
		after_project_agent_id,after_project_id,after_key,after_display_label,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operation, actorUserID, requestID, beforeRevision, afterRevision,
		beforeAgent, beforeProject, beforeKey, beforeLabel, afterAgent, afterProject, afterKey, afterLabel, createdAt)
	return err
}

func scanOrchestratorEvent(row interface{ Scan(...any) error }) (models.OrchestratorEvent, error) {
	var event models.OrchestratorEvent
	var beforeAgent, beforeProject, afterAgent, afterProject sql.NullInt64
	var beforeKey, beforeLabel, afterKey, afterLabel sql.NullString
	err := row.Scan(&event.EventID, &event.Operation, &event.ActorUserID, &event.RequestID,
		&event.BeforeRevision, &event.AfterRevision, &beforeAgent, &beforeProject, &beforeKey, &beforeLabel,
		&afterAgent, &afterProject, &afterKey, &afterLabel, &event.CreatedAt)
	if err != nil {
		return event, err
	}
	if beforeAgent.Valid {
		event.Before = &models.OrchestratorTarget{ProjectID: beforeProject.Int64, ProjectAgentID: beforeAgent.Int64,
			Key: beforeKey.String, DisplayLabel: beforeLabel.String}
	}
	if afterAgent.Valid {
		event.After = &models.OrchestratorTarget{ProjectID: afterProject.Int64, ProjectAgentID: afterAgent.Int64,
			Key: afterKey.String, DisplayLabel: afterLabel.String}
	}
	return event, nil
}

func parseOrchestratorEventQuery(uri *url.URL) (int64, int64, bool) {
	values, err := url.ParseQuery(uri.RawQuery)
	if err != nil {
		return 0, 0, false
	}
	for key, entries := range values {
		if (key != "after_revision" && key != "limit") || len(entries) != 1 {
			return 0, 0, false
		}
	}
	after, limit := int64(0), int64(50)
	if entries, exists := values["after_revision"]; exists {
		parsed, ok := strictQueryInt(entries[0], 0, math.MaxInt64)
		if !ok {
			return 0, 0, false
		}
		after = parsed
	}
	if entries, exists := values["limit"]; exists {
		parsed, ok := strictQueryInt(entries[0], 1, 100)
		if !ok {
			return 0, 0, false
		}
		limit = parsed
	}
	return after, limit, true
}

func strictQueryInt(raw string, min, max int64) (int64, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value >= min && value <= max && strconv.FormatInt(value, 10) == raw
}

func canonicalOrchestratorTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func orchestratorRequestID(r *http.Request) string {
	value := strings.TrimSpace(requestIDFromRequest(r))
	if len(value) == 0 || len(value) > orchestratorRequestIDMax || !utf8.ValidString(value) {
		return newAIRequestID()
	}
	for _, char := range value {
		if char <= 31 || char == 127 {
			return newAIRequestID()
		}
	}
	return value
}

func orchestratorConfigResponse(state orchestratorState) models.OrchestratorConfigResponse {
	return models.OrchestratorConfigResponse{SchemaVersion: orchestratorSchemaVersion, Revision: state.Revision,
		Orchestrator: state.Target, UpdatedAt: state.UpdatedAt}
}

func writeOrchestratorJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeOrchestratorError(w http.ResponseWriter, status int, code string) {
	writeOrchestratorJSON(w, status, map[string]string{"error": code})
}

func orchestratorInternalError(w http.ResponseWriter, err error) {
	log.Printf("orchestrator operation failed: %v", err)
	writeOrchestratorError(w, http.StatusInternalServerError, "internal_error")
}

func isOrchestratorProjectConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "assigned orchestrator project")
}

func isOrchestratorHarnessConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "assigned orchestrator has active harness")
}
