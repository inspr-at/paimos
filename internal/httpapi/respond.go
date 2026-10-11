// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
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
		// A fabricated driver error must not smuggle arbitrary text through
		// its Code field. SQLSTATE is exactly five uppercase ASCII letters/digits.
		if len(pgErr.Code) != 5 {
			return "postgres_unknown"
		}
		for _, ch := range pgErr.Code {
			if !(ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z') {
				return "postgres_unknown"
			}
		}
		return "postgres_" + pgErr.Code
	case errors.Is(err, pgconn.ErrConnClosed):
		return "connection_closed"
	case errors.Is(err, pgx.ErrNoRows):
		return "no_rows"
	default:
		var network net.Error
		if errors.As(err, &network) {
			return "network_error"
		}
		return "internal"
	}
}

// LogFailure records only fixed steps, route templates and error classes.
// Error messages, URL values, credentials and response bodies stay out of logs.
// Call at the failure site so a live request still retains a database SQLSTATE;
// the outer status writer also provides a fallback for otherwise silent 5xx.
func LogFailure(r *http.Request, step string, err error) {
	if marker, ok := r.Context().Value(failureKey{}).(*bool); ok {
		*marker = true
	}
	class := ErrorClass(err)
	if err == nil {
		class = "internal"
	}
	slog.ErrorContext(r.Context(), "request failed", "request_id", RequestID(r.Context()),
		"route", requestLogRoute(r), "step", step, "error_class", class,
		"request_context", ErrorClass(r.Context().Err()))
}

type failureKey struct{}
