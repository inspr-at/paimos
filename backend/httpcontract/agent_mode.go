// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package httpcontract contains small dependency-neutral HTTP contracts shared
// by authentication middleware and endpoint handlers.
package httpcontract

import (
	"encoding/json"
	"net/http"
	"strings"
)

const requestIDHeader = "X-PAIMOS-Request-Id"

// WriteAgentModeNotFound is intentionally the one 404 representation for an
// external account, missing root, inaccessible project, selector, or detail.
// Keeping it below both auth and handlers avoids an import cycle and makes
// existence-hiding byte-for-byte testable after URI/request-id normalization.
func WriteAgentModeNotFound(w http.ResponseWriter, r *http.Request) {
	payload := struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		Status    int    `json:"status"`
		Detail    string `json:"detail"`
		Instance  string `json:"instance,omitempty"`
		Code      string `json:"code"`
		RequestID string `json:"request_id,omitempty"`
		Error     string `json:"error"`
	}{
		Type: "https://paimos.com/errors/not_found", Title: http.StatusText(http.StatusNotFound),
		Status: http.StatusNotFound, Detail: "not found", Code: "not_found", Error: "not found",
	}
	// Ordinary Agent Mode keeps its long-standing instance URI. A frozen
	// supervisory-control route omits it because the path and query carry
	// delivery/run/command identifiers that must not be reflected.
	if r != nil && !IsControlRequest(r) {
		payload.Instance = r.URL.RequestURI()
	}
	payload.RequestID = strings.TrimSpace(w.Header().Get(requestIDHeader))
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(payload)
}
