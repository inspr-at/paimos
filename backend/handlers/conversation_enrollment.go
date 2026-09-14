// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/conversationturns"
	"github.com/inspr-at/paimos/backend/db"
)

const conversationServiceMaximumLifetime = 366 * 24 * time.Hour

type conversationEnrollmentRequest struct {
	conversationturns.Enrollment
	AgeRecipients json.RawMessage `json:"age_recipients"`
}

func CreateConversationService(w http.ResponseWriter, r *http.Request) {
	SetControlCachePolicy(w)
	var request conversationEnrollmentRequest
	if err := DecodeControlJSON(w, r, 96*1024, &request); err != nil {
		conversationError(w, conversationturns.ErrInvalid)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	expires, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(request.ExpiresAt))
	now := time.Now().UTC()
	if err != nil || !expires.After(now) || expires.After(now.Add(conversationServiceMaximumLifetime)) {
		conversationError(w, conversationturns.ErrInvalid)
		return
	}
	var recipients []age.Recipient
	ageDelivery := request.AgeRecipients != nil
	if ageDelivery {
		if bytes.Equal(bytes.TrimSpace(request.AgeRecipients), []byte("null")) {
			conversationError(w, conversationturns.ErrInvalid)
			return
		}
		var encoded []string
		if json.Unmarshal(request.AgeRecipients, &encoded) != nil {
			conversationError(w, conversationturns.ErrInvalid)
			return
		}
		recipients, err = parseMachineNotifierAgeRecipients(encoded)
		if err != nil {
			conversationError(w, conversationturns.ErrInvalid)
			return
		}
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		conversationError(w, conversationturns.ErrStorage)
		return
	}
	defer tx.Rollback()
	user, principal, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, now)
	if err != nil || user == nil || principal.Kind() != auth.PrincipalSession || principal.Impersonated() || !auth.IsAdmin(user) {
		conversationError(w, conversationturns.ErrUnavailable)
		return
	}
	full, prefix, hash, err := generateAPIKey()
	if err != nil {
		conversationError(w, conversationturns.ErrStorage)
		return
	}
	var encrypted string
	if ageDelivery {
		encrypted, err = encryptMachineNotifierCredential(full, recipients)
		if err != nil {
			conversationError(w, conversationturns.ErrStorage)
			return
		}
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO api_keys(
		user_id,name,key_hash,key_prefix,scopes,expires_at,credential_kind)
		VALUES(?,?,?,?,?,?, 'general')`, user.ID, request.Name, hash, prefix, "", timestampForCredential(expires))
	if err != nil {
		conversationError(w, conversationturns.ErrStorage)
		return
	}
	keyID, err := result.LastInsertId()
	if err != nil {
		conversationError(w, conversationturns.ErrStorage)
		return
	}
	binding, err := conversationturns.NewService(db.DB).EnrollTx(r.Context(), tx, principal, keyID, request.Enrollment)
	if err != nil {
		conversationError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		conversationError(w, conversationturns.ErrStorage)
		return
	}
	log.Printf("audit: conversation_service_created key_id=%d project_id=%d binding_id=%s runtime_id=%s",
		keyID, binding.ProjectID, binding.ID, binding.RuntimeID)
	response := map[string]any{
		"schema_version": 1, "id": keyID, "name": request.Name, "key_prefix": prefix,
		"expires_at": expires.UTC().Format(time.RFC3339Nano), "binding": binding,
	}
	if ageDelivery {
		response["credential_delivery"] = "age"
		response["key_age_base64"] = encrypted
	} else {
		response["key"] = full
	}
	writeHarnessJSON(w, http.StatusCreated, response)
}

func timestampForCredential(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
