// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

const machineNotifierMaximumLifetime = 366 * 24 * time.Hour

type createMachineNotifierRequest struct {
	Name          string `json:"name"`
	ProjectID     int64  `json:"project_id"`
	Sender        string `json:"sender"`
	To            string `json:"to"`
	TargetID      string `json:"target_id"`
	TargetVersion int    `json:"target_version"`
	ExpiresAt     string `json:"expires_at"`
}

type machineNotifierSendRequest struct {
	Body string `json:"body"`
}

func CreateMachineNotifier(w http.ResponseWriter, r *http.Request) {
	var req createMachineNotifierRequest
	if !decodeSingleJSON(w, r, 8192, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]byte(req.Name)) > 128 || req.ProjectID <= 0 || req.TargetVersion <= 0 {
		messageProblem(w, r, "machine_notifier_request_invalid", "machine notifier enrollment is invalid", http.StatusBadRequest)
		return
	}
	expires, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.ExpiresAt))
	now := time.Now().UTC()
	if err != nil || !expires.After(now) || expires.After(now.Add(machineNotifierMaximumLifetime)) {
		messageProblem(w, r, "machine_notifier_expiry_invalid", "expires_at must be a future RFC 3339 timestamp within 366 days", http.StatusBadRequest)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	user, principal, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, now)
	if err != nil || user == nil || principal.Kind() != auth.PrincipalSession || !auth.IsAdmin(user) || principal.Impersonated() {
		messageProblem(w, r, "machine_notifier_operator_required", "a current non-impersonated administrator session is required", http.StatusForbidden)
		return
	}
	full, prefix, hash, err := generateAPIKey()
	if err != nil {
		jsonError(w, "key generation failed", http.StatusInternalServerError)
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO api_keys
		(user_id,name,key_hash,key_prefix,scopes,expires_at,credential_kind)
		VALUES(?,?,?,?,?,?, 'machine_notifier')`, user.ID, req.Name, hash, prefix, "", expires.UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	keyID, err := result.LastInsertId()
	if err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	if err := agentmessage.CreateMachineNotifierBindingTx(r.Context(), tx, agentmessage.MachineNotifierBindingInput{
		APIKeyID: keyID, ProjectID: req.ProjectID, Sender: req.Sender, Address: req.To,
		TargetID: req.TargetID, TargetVersion: req.TargetVersion,
	}); err != nil {
		writeMachineNotifierError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	log.Printf("audit: machine_notifier_created username=%q key_id=%d project_id=%d target_id=%s target_version=%d",
		user.Username, keyID, req.ProjectID, strings.TrimSpace(req.TargetID), req.TargetVersion)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": keyID, "name": req.Name, "key_prefix": prefix, "key": full,
		"expires_at": expires.UTC().Format("2006-01-02T15:04:05.000Z"),
		"binding": map[string]any{"project_id": req.ProjectID, "sender": strings.TrimSpace(req.Sender),
			"to": strings.TrimSpace(req.To), "target_id": strings.TrimSpace(req.TargetID), "target_version": req.TargetVersion},
	})
}

func SendMachineNotifierMessage(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get(AgentNameHeader)) != "" || strings.TrimSpace(r.Header.Get(SessionHeader)) != "" {
		messageProblem(w, r, "machine_notifier_attribution_refused", "machine notifier attribution is fixed by its binding", http.StatusBadRequest)
		return
	}
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" || len([]byte(values[0])) > 128 {
		messageProblem(w, r, "machine_notifier_idempotency_required", "exactly one Idempotency-Key of at most 128 bytes is required", http.StatusBadRequest)
		return
	}
	var req machineNotifierSendRequest
	if !decodeSingleJSON(w, r, agentmessage.MaxBodySize+1024, &req) {
		return
	}
	authority := machineNotifierAuthority(r)
	message, err := agentmessage.NewService(db.DB).SendEnvelope(r.Context(), agentmessage.SendEnvelopeInput{
		Body: req.Body, IdempotencyKey: values[0], NotifierAuthority: authority,
	})
	if err != nil {
		writeMachineNotifierError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"message_id": message.MessageID})
}

func GetMachineNotifierReceipt(w http.ResponseWriter, r *http.Request) {
	messageID := strings.TrimSpace(chi.URLParam(r, "messageID"))
	if _, err := uuid.Parse(messageID); err != nil {
		messageProblem(w, r, "machine_notifier_receipt_unavailable", "machine notifier receipt is unavailable", http.StatusNotFound)
		return
	}
	receipt, err := agentmessage.NewService(db.DB).MachineNotifierReceipt(r.Context(), messageID, machineNotifierAuthority(r))
	if err != nil {
		writeMachineNotifierError(w, r, err)
		return
	}
	jsonOK(w, receipt)
}

func machineNotifierAuthority(r *http.Request) agentmessage.NotifierAuthority {
	return func(ctx context.Context, tx *sql.Tx) (int64, error) {
		_, principal, err := auth.ReauthorizeRequestPrincipalTx(ctx, tx, r, time.Now().UTC())
		if err != nil || principal.Kind() != auth.PrincipalMachineNotifier || principal.Impersonated() {
			return 0, auth.ErrCredentialUnavailable
		}
		return principal.APIKeyID(), nil
	}
}

func decodeSingleJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		messageProblem(w, r, "machine_notifier_request_invalid", "machine notifier request is invalid", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		messageProblem(w, r, "machine_notifier_request_invalid", "machine notifier request must contain one JSON object", http.StatusBadRequest)
		return false
	}
	return true
}

func writeMachineNotifierError(w http.ResponseWriter, r *http.Request, err error) {
	code, status := "machine_notifier_request_invalid", http.StatusBadRequest
	var coded *agentmessage.CodedError
	recognized := errors.As(err, &coded)
	if recognized {
		code = coded.Code
		switch code {
		case "machine_notifier_unauthorized":
			status = http.StatusUnauthorized
		case "machine_notifier_receipt_unavailable":
			status = http.StatusNotFound
		case "machine_notifier_binding_unavailable", "machine_notifier_target_changed":
			status = http.StatusConflict
		}
	}
	if errors.Is(err, agentmessage.ErrBodyTooLarge) {
		code, status = "agent_message_body_too_large", http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, agentmessage.ErrContainsSecret) {
		code = "agent_message_secret_rejected"
	}
	if errors.Is(err, agentmessage.ErrRateLimitExceeded) {
		code, status, recognized = "agent_message_rate_limited", http.StatusTooManyRequests, true
	}
	if !recognized && !errors.Is(err, agentmessage.ErrBodyTooLarge) && !errors.Is(err, agentmessage.ErrContainsSecret) {
		log.Printf("machine notifier request failed: %T", err)
		jsonError(w, "machine notifier unavailable", http.StatusInternalServerError)
		return
	}
	messageProblem(w, r, code, err.Error(), status)
}
