// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	sessionUtteranceSchemaVersion = 1
	sessionUtteranceMaxBodyBytes  = 16 * 1024
	sessionUtteranceMaxTextBytes  = 8 * 1024
)

var sessionUtteranceIDPattern = regexp.MustCompile(`^utt_[0-9a-f]{32}$`)

type sessionUtteranceWire struct {
	SchemaVersion   int             `json:"schema_version"`
	UtteranceID     string          `json:"utterance_id"`
	Text            string          `json:"text"`
	SelectedSession json.RawMessage `json:"selected_session"`
}

type sessionUtteranceSelection struct {
	ProductSessionID string `json:"product_session_id"`
	Revision         int64  `json:"revision"`
}

type sessionUtteranceResult struct {
	SchemaVersion          int     `json:"schema_version"`
	UtteranceID            string  `json:"utterance_id"`
	RouteKind              string  `json:"route_kind"`
	ProductSessionID       string  `json:"product_session_id"`
	ProductSessionRevision int64   `json:"product_session_revision"`
	MessageID              string  `json:"message_id"`
	ThreadID               string  `json:"thread_id"`
	DeliveryID             *string `json:"delivery_id"`
	CreatedAt              string  `json:"created_at"`
}

func SessionUtteranceV1(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	request, selection, code, status := decodeSessionUtterance(w, r)
	if code != "" {
		sessionUtteranceProblem(w, r, code, status)
		return
	}
	instance := strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_INSTANCE"))
	if len([]byte(instance)) < 1 || len([]byte(instance)) > 64 {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	digest, err := sessionUtteranceDigest(projectID, request.Text, selection)
	if err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_request_invalid", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("session utterance v1: insert canonical message: %v", err)
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		// The outer middleware already emitted the public credential contract.
		// A commit-time credential change is concealed as a missing resource.
		sessionUtteranceProblem(w, r, "session_utterance_not_found", http.StatusNotFound)
		return
	}
	allowed, err := canEditProjectTx(r.Context(), tx, user, projectID)
	if err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	if !allowed {
		sessionUtteranceProblem(w, r, "session_utterance_not_found", http.StatusNotFound)
		return
	}
	if prior, priorDigest, found, err := loadSessionUtteranceReceipt(r.Context(), tx, instance, projectID, user.ID, request.UtteranceID); err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	} else if found {
		if !bytes.Equal(priorDigest, digest) {
			sessionUtteranceProblem(w, r, "session_utterance_idempotency_conflict", http.StatusConflict)
			return
		}
		if err := tx.Commit(); err != nil {
			sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
			return
		}
		writeSessionUtteranceResult(w, prior)
		return
	}

	route, errCode, errStatus := resolveSessionUtteranceRoute(r.Context(), tx, projectID, user, selection, instance)
	if errCode != "" {
		sessionUtteranceProblem(w, r, errCode, errStatus)
		return
	}
	messageUUID, err := uuid.NewV7()
	if err != nil {
		messageUUID = uuid.New()
	}
	deliveryUUID := uuid.Nil
	if route.TargetAgentID != nil {
		deliveryUUID, err = uuid.NewV7()
		if err != nil {
			deliveryUUID = uuid.New()
		}
	}
	createdAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	parts, _ := json.Marshal([]map[string]string{{"kind": "text", "text": request.Text}})
	fromAddress := fmt.Sprintf("user:%d", user.ID)
	result, err := tx.ExecContext(r.Context(), `INSERT INTO agent_messages(
		from_agent_id,to_agent_id,hop_count,body,is_action_request,delivered,held_reason,created_at,delivered_at,
		message_id,context_id,task_id,role,parts_json,metadata_json,from_address,to_address,reply_to,thread_id,
		session_id,read_at,delivery_level,delivery_fallback,delivery_primary_target_id,delivery_fallback_target_id,
		from_user_id,product_session_id)
		VALUES(NULL,?,1,?,0,1,'',?,?,? ,?,'','human',?,'{}',?,?,'',?,'',NULL,'simple','simple',?,NULL,?,?)`,
		nullableSessionUtteranceAgentID(route.TargetAgentID), request.Text, createdAt, createdAt, messageUUID.String(), route.ProjectKey,
		string(parts), fromAddress, route.ToAddress, route.ProductSessionID, nullableStringValue(route.TargetID),
		user.ID, route.ProductSessionID)
	if err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	messageRowID, err := result.LastInsertId()
	if err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	var deliveryID any
	if route.TargetAgentID != nil {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO agent_message_deliveries(
			delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
			VALUES(?,?,?,?, 'simple','pending')`, deliveryUUID.String(), messageRowID, instance, route.TargetID); err != nil {
			sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
			return
		}
		deliveryID = deliveryUUID.String()
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO session_utterance_receipts(
		instance,project_id,user_id,utterance_id,request_digest,message_row_id,product_session_id,
		product_session_revision,delivery_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		instance, projectID, user.ID, request.UtteranceID, digest, messageRowID, route.ProductSessionID,
		route.Revision, deliveryID, createdAt); err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		sessionUtteranceProblem(w, r, "session_utterance_write_failed", http.StatusInternalServerError)
		return
	}
	out := sessionUtteranceResult{
		SchemaVersion: sessionUtteranceSchemaVersion, UtteranceID: request.UtteranceID,
		RouteKind: route.Kind, ProductSessionID: route.ProductSessionID,
		ProductSessionRevision: route.Revision, MessageID: messageUUID.String(),
		ThreadID: route.ProductSessionID, CreatedAt: createdAt,
	}
	if route.TargetAgentID != nil {
		value := deliveryUUID.String()
		out.DeliveryID = &value
	}
	writeSessionUtteranceResult(w, out)
}

type resolvedSessionUtteranceRoute struct {
	Kind             string
	ProjectKey       string
	ProductSessionID string
	Revision         int64
	TargetAgentID    *int64
	ToAddress        string
	TargetID         string
}

func resolveSessionUtteranceRoute(ctx context.Context, tx *sql.Tx, projectID int64, user *models.User, selection *sessionUtteranceSelection, instance string) (resolvedSessionUtteranceRoute, string, int) {
	var route resolvedSessionUtteranceRoute
	if err := tx.QueryRowContext(ctx, `SELECT key FROM projects WHERE id=? AND status<>'deleted'`, projectID).Scan(&route.ProjectKey); err != nil {
		return route, "session_utterance_not_found", http.StatusNotFound
	}
	if selection == nil {
		var sessionID string
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT binding.product_session_id,ps.revision
			FROM paimos_conversation_bindings binding JOIN product_sessions ps ON ps.product_session_id=binding.product_session_id
			WHERE binding.project_id=? AND binding.user_id=? AND ps.project_id=? AND ps.target_kind='paimos'`, projectID, user.ID, projectID).
			Scan(&sessionID, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			// Product-session identity is the existing v4 public contract. Message
			// and delivery identities may use v7, but this table deliberately does
			// not change identity versions as part of the utterance slice.
			sessionID = uuid.NewString()
			if _, err := tx.ExecContext(ctx, `INSERT INTO product_sessions(
				product_session_id,project_id,target_kind,target_project_agent_id,node_id,title,summary,revision,
				created_by_user_id,updated_by_user_id)
				VALUES(?,?,'paimos',NULL,NULL,'Paimos conversation','',1,?,?)`, sessionID, projectID, user.ID, user.ID); err != nil {
				log.Printf("session utterance v1: create Paimos product session: %v", err)
				return route, "session_utterance_write_failed", http.StatusInternalServerError
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO paimos_conversation_bindings(project_id,user_id,product_session_id)
				VALUES(?,?,?)`, projectID, user.ID, sessionID); err != nil {
				log.Printf("session utterance v1: bind Paimos conversation: %v", err)
				return route, "session_utterance_write_failed", http.StatusInternalServerError
			}
			revision = 1
		} else if err != nil {
			return route, "session_utterance_write_failed", http.StatusInternalServerError
		}
		route.Kind, route.ProductSessionID, route.Revision, route.ToAddress = "paimos", sessionID, revision, "paimos"
		return route, "", 0
	}

	var targetAgentID, nodeID sql.NullInt64
	var targetKind, agentName string
	if err := tx.QueryRowContext(ctx, `SELECT ps.revision,ps.target_kind,ps.target_project_agent_id,pa.name,ps.node_id
		FROM product_sessions ps
		JOIN project_agents pa ON pa.id=ps.target_project_agent_id AND pa.project_id=ps.project_id
		WHERE ps.project_id=? AND ps.product_session_id=?`, projectID, selection.ProductSessionID).
		Scan(&route.Revision, &targetKind, &targetAgentID, &agentName, &nodeID); errors.Is(err, sql.ErrNoRows) {
		return route, "session_utterance_not_found", http.StatusNotFound
	} else if err != nil {
		return route, "session_utterance_write_failed", http.StatusInternalServerError
	}
	if targetKind != "project_agent" || !targetAgentID.Valid {
		return route, "session_utterance_not_found", http.StatusNotFound
	}
	if route.Revision != selection.Revision {
		return route, "session_utterance_selection_stale", http.StatusConflict
	}
	if nodeID.Valid {
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id=? AND project_id=? AND deleted_at IS NULL`, nodeID.Int64, projectID).Scan(&live); err != nil {
			return route, "session_utterance_write_failed", http.StatusInternalServerError
		}
		if live != 1 {
			return route, "session_utterance_not_found", http.StatusNotFound
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT hs.harness,t.id
		FROM harness_sessions hs JOIN agent_message_targets t ON t.id=hs.message_target_id
		WHERE hs.project_id=? AND hs.project_agent_id=? AND hs.agent_name=? AND hs.phase<>'stopped'
		 AND hs.advertised_inbox=1 AND t.instance=? AND t.project_id=hs.project_id AND t.enabled=1
		 AND t.address=lower(hs.harness)||':'||hs.agent_name
		 AND (CASE WHEN hs.phase='starting' THEN julianday(hs.updated_at)>=julianday('now','-90 seconds')
		           ELSE hs.heartbeat_at IS NOT NULL AND julianday(hs.heartbeat_at)>=julianday('now','-90 seconds') END)
		ORDER BY hs.id`, projectID, targetAgentID.Int64, agentName, instance)
	if err != nil {
		return route, "session_utterance_write_failed", http.StatusInternalServerError
	}
	defer rows.Close()
	type candidate struct{ harness, targetID string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.harness, &item.targetID); err != nil {
			return route, "session_utterance_write_failed", http.StatusInternalServerError
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return route, "session_utterance_write_failed", http.StatusInternalServerError
	}
	if err := rows.Close(); err != nil {
		return route, "session_utterance_write_failed", http.StatusInternalServerError
	}
	if len(candidates) != 1 {
		return route, "session_utterance_target_unavailable", http.StatusConflict
	}
	id := targetAgentID.Int64
	route.Kind, route.ProductSessionID, route.TargetAgentID = "project_agent", selection.ProductSessionID, &id
	route.ToAddress, route.TargetID = strings.ToLower(candidates[0].harness)+":"+agentName, candidates[0].targetID
	return route, "", 0
}

func decodeSessionUtterance(w http.ResponseWriter, r *http.Request) (sessionUtteranceWire, *sessionUtteranceSelection, string, int) {
	var request sessionUtteranceWire
	reader := http.MaxBytesReader(w, r.Body, sessionUtteranceMaxBodyBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, nil, "session_utterance_payload_too_large", http.StatusRequestEntityTooLarge
		}
		return request, nil, "session_utterance_request_invalid", http.StatusBadRequest
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request, nil, "session_utterance_request_invalid", http.StatusBadRequest
	}
	if request.SchemaVersion != sessionUtteranceSchemaVersion || len(request.SelectedSession) == 0 {
		return request, nil, "session_utterance_request_invalid", http.StatusBadRequest
	}
	if !sessionUtteranceIDPattern.MatchString(request.UtteranceID) {
		return request, nil, "session_utterance_id_invalid", http.StatusBadRequest
	}
	if !validSessionUtteranceText(request.Text) {
		return request, nil, "session_utterance_text_invalid", http.StatusBadRequest
	}
	if bytes.Equal(bytes.TrimSpace(request.SelectedSession), []byte("null")) {
		return request, nil, "", 0
	}
	var selection sessionUtteranceSelection
	selectedDecoder := json.NewDecoder(bytes.NewReader(request.SelectedSession))
	selectedDecoder.DisallowUnknownFields()
	if err := selectedDecoder.Decode(&selection); err != nil {
		return request, nil, "session_utterance_request_invalid", http.StatusBadRequest
	}
	if err := selectedDecoder.Decode(&struct{}{}); err != io.EOF || selection.Revision <= 0 {
		return request, nil, "session_utterance_request_invalid", http.StatusBadRequest
	}
	parsed, err := uuid.Parse(selection.ProductSessionID)
	if err != nil || parsed.String() != selection.ProductSessionID {
		return request, nil, "session_utterance_request_invalid", http.StatusBadRequest
	}
	return request, &selection, "", 0
}

func validSessionUtteranceText(text string) bool {
	if !utf8.ValidString(text) || text == "" || text != strings.TrimSpace(text) || len([]byte(text)) > sessionUtteranceMaxTextBytes {
		return false
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func sessionUtteranceDigest(projectID int64, text string, selection *sessionUtteranceSelection) ([]byte, error) {
	canonical := struct {
		Domain    string                     `json:"domain"`
		ProjectID int64                      `json:"project_id"`
		Text      string                     `json:"text"`
		Selection *sessionUtteranceSelection `json:"selected_session"`
	}{"paimos.session-utterance.v1", projectID, text, selection}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func loadSessionUtteranceReceipt(ctx context.Context, tx *sql.Tx, instance string, projectID, userID int64, utteranceID string) (sessionUtteranceResult, []byte, bool, error) {
	var result sessionUtteranceResult
	var digest []byte
	var deliveryID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT receipt.request_digest,receipt.product_session_id,
		receipt.product_session_revision,receipt.delivery_id,receipt.created_at,message.message_id
		FROM session_utterance_receipts receipt JOIN agent_messages message ON message.id=receipt.message_row_id
		WHERE receipt.instance=? AND receipt.project_id=? AND receipt.user_id=? AND receipt.utterance_id=?`,
		instance, projectID, userID, utteranceID).
		Scan(&digest, &result.ProductSessionID, &result.ProductSessionRevision, &deliveryID, &result.CreatedAt, &result.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil, false, nil
	}
	if err != nil {
		return result, nil, false, err
	}
	result.SchemaVersion, result.UtteranceID, result.ThreadID = sessionUtteranceSchemaVersion, utteranceID, result.ProductSessionID
	result.RouteKind = "paimos"
	if deliveryID.Valid {
		result.RouteKind = "project_agent"
		value := deliveryID.String
		result.DeliveryID = &value
	}
	return result, digest, true, nil
}

func sessionUtteranceProblem(w http.ResponseWriter, r *http.Request, code string, status int) {
	problemJSON(w, r, ProblemDetails{Type: "https://paimos.com/errors/" + code, Title: http.StatusText(status), Status: status, Detail: code, Code: code})
}

func writeSessionUtteranceResult(w http.ResponseWriter, result sessionUtteranceResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(result)
}

func nullableSessionUtteranceAgentID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableStringValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}
