// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/flowhost"
)

func baselineFlowState(w http.ResponseWriter, r *http.Request) {
	state, _, ok := loadCurrentFlowState(w, r)
	if !ok {
		return
	}
	jsonOK(w, state)
}

func baselineFlowIntents(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<10+1))
	if err != nil || len(raw) == 0 || len(raw) > 32<<10 {
		jsonError(w, "invalid flow intent", http.StatusBadRequest)
		return
	}
	var intent flowhost.IntentRequest
	if json.Unmarshal(raw, &intent) != nil {
		jsonError(w, "invalid flow intent", http.StatusBadRequest)
		return
	}
	state, principal, ok := loadCurrentFlowState(w, r)
	if !ok {
		return
	}
	projectID, _ := baselineBatchProject(r)
	result, status, handleErr := flowhost.Handle(state, principal, projectID, intent, time.Time{})
	if handleErr != nil || status != http.StatusOK {
		writeFlowIntent(w, result, status)
		return
	}
	writeFlowIntent(w, result, status)
}

func loadCurrentFlowState(w http.ResponseWriter, r *http.Request) (*flowhost.ShellState, auth.Principal, bool) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	principal, havePrincipal := auth.GetPrincipal(r)
	if !ok || !okID || !havePrincipal {
		jsonError(w, "not found", http.StatusNotFound)
		return nil, auth.Principal{}, false
	}
	workflow, err := baselineBatchService(r).Workflow(r.Context(), actor, projectID)
	if err != nil {
		baselineBatchError(w, err)
		return nil, auth.Principal{}, false
	}
	if !workflow.INSPRStreamEnabled {
		jsonError(w, "not found", http.StatusNotFound)
		return nil, auth.Principal{}, false
	}
	state, err := flowhost.Build(flowhost.BuildInput{
		Principal:        principal,
		PermissionsEpoch: auth.GetPermissionsEpoch(principal.UserID()),
		ProjectID:        projectID,
		ProjectName:      loadProjectFlowLabel(projectID),
		Workflow:         workflow,
	})
	if errors.Is(err, flowhost.ErrNotEnabled) {
		jsonError(w, "not found", http.StatusNotFound)
		return nil, auth.Principal{}, false
	}
	if err != nil {
		jsonError(w, "flow context unavailable", http.StatusInternalServerError)
		return nil, auth.Principal{}, false
	}
	return state, principal, true
}

func loadProjectFlowLabel(projectID int64) string {
	var name string
	if err := db.DB.QueryRow(`SELECT name FROM projects WHERE id=?`, projectID).Scan(&name); err != nil {
		return "Project"
	}
	return name
}

func writeFlowIntent(w http.ResponseWriter, result flowhost.IntentResult, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
