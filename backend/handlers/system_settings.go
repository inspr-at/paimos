package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/backend/db"
)

type systemSettingsPayload struct {
	UndoStackDepth int `json:"undo_stack_depth"`
	// PAI-706: instance default for the voice-intake auto-switch
	// confidence threshold (50..100); per-user override on the profile.
	IntakeConfidenceThreshold int `json:"intake_confidence_threshold"`
	// PAI-714: per-session AI token budget for intake (1k..500k).
	IntakeSessionTokenBudget int `json:"intake_session_token_budget"`
}

func GetSystemSettings(w http.ResponseWriter, r *http.Request) {
	depth, err := loadUndoStackDepth(db.DB)
	if err != nil {
		log.Printf("system settings load: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, systemSettingsPayload{
		UndoStackDepth:            depth,
		IntakeConfidenceThreshold: intakeConfidenceThresholdInstance(r.Context()),
		IntakeSessionTokenBudget:  intakeSessionTokenBudgetFor(r.Context()),
	})
}

func PutSystemSettings(w http.ResponseWriter, r *http.Request) {
	var body systemSettingsPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.UndoStackDepth < 1 || body.UndoStackDepth > 20 {
		jsonError(w, "undo_stack_depth must be between 1 and 20", http.StatusBadRequest)
		return
	}
	if body.IntakeConfidenceThreshold == 0 {
		body.IntakeConfidenceThreshold = intakeConfidenceThreshold
	}
	if body.IntakeConfidenceThreshold < 50 || body.IntakeConfidenceThreshold > 100 {
		jsonError(w, "intake_confidence_threshold must be between 50 and 100", http.StatusBadRequest)
		return
	}
	if body.IntakeSessionTokenBudget == 0 {
		body.IntakeSessionTokenBudget = intakeSessionTokenBudgetDefault
	}
	if body.IntakeSessionTokenBudget < 1000 || body.IntakeSessionTokenBudget > 500000 {
		jsonError(w, "intake_session_token_budget must be between 1000 and 500000", http.StatusBadRequest)
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO app_settings(key, value, updated_at) VALUES('undo_stack_depth', ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=datetime('now')`,
		strings.TrimSpace(strconv.Itoa(body.UndoStackDepth)),
	); err != nil {
		log.Printf("system settings save: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO app_settings(key, value, updated_at) VALUES('intake_confidence_threshold', ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=datetime('now')`,
		strconv.Itoa(body.IntakeConfidenceThreshold),
	); err != nil {
		log.Printf("system settings save: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO app_settings(key, value, updated_at) VALUES('intake_session_token_budget', ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=datetime('now')`,
		strconv.Itoa(body.IntakeSessionTokenBudget),
	); err != nil {
		log.Printf("system settings save: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, body)
}
