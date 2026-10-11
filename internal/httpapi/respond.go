// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/tenant"
)

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "err", err)
	}
}

// WriteError writes a JSON error body: {"error": message}.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func handleNotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, "not found")
}

// RetiredFlow preserves authenticated route compatibility without touching storage.
func RetiredFlow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := tenant.PrincipalFrom(r.Context()); !ok {
		WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	WriteError(w, http.StatusGone, "The INSPR Flow is retired.")
}

// ErrorClass names a failure without its text, so 5xx answers can be logged
// next to their request id without leaking SQL, identifiers or payload
// content. An already cancelled request context is the AEON-1228 signature.
func ErrorClass(err error) string {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	case errors.As(err, &pgErr):
		return "postgres_" + pgErr.Code
	case errors.Is(err, pgx.ErrNoRows):
		return "no_rows"
	default:
		return "internal"
	}
}
