// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"io"
	"net/http"
)

type errorJSON struct {
	Error string `json:"error"`
}

const notMemberSentence = "Not a member of this workspace yet"

const signInFailedPage = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Sign-in failed</title></head>
<body><p>Sign-in failed</p></body>
</html>
`

const notReadyPage = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Workspace not ready</title></head>
<body><p>This workspace is not ready</p></body>
</html>
`

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, errorJSON{Error: "unauthorized"})
}

func writeForbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, errorJSON{Error: "forbidden"})
}

func writeBadRequest(w http.ResponseWriter, msg string) {
	if msg == "" {
		msg = "bad request"
	}
	writeJSON(w, http.StatusBadRequest, errorJSON{Error: msg})
}

func writeInternal(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, errorJSON{Error: "internal"})
}

// /api/me advertises public sign-in configuration even before a session exists.
func (m *Module) writeMeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, struct {
		Error           string `json:"error"`
		DevMode         bool   `json:"dev_mode"`
		OIDCDisplayName string `json:"oidc_display_name"`
	}{Error: "unauthorized", DevMode: m.cfg.Dev(), OIDCDisplayName: m.cfg.OIDCDisplayName})
}
