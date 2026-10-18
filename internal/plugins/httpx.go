// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ErrClosed means the installation is missing, disabled, or pinned to a
// different digest. Callers fail closed and do not start work.
var ErrClosed = errors.New("plugin installation is closed")

// ErrDenied means the installation does not grant a permission this call needs.
var ErrDenied = errors.New("plugin permission denied")

// ErrLeaseHeld means this tenant already holds the job lease.
var ErrLeaseHeld = errors.New("plugin job lease is held")

// Error is a client-facing HTTP error.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(status int, code, message string) error {
	return &Error{Status: status, Code: code, Message: message}
}

func invalid(message string) error { return fail(http.StatusBadRequest, "invalid_request", message) }

func forbidden(message string) error { return fail(http.StatusForbidden, "forbidden", message) }

func notFound(message string) error { return fail(http.StatusNotFound, "not_found", message) }

func conflict(message string) error { return fail(http.StatusConflict, "conflict", message) }

func unauthorized() error {
	return fail(http.StatusUnauthorized, "unauthorized", "authentication required")
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		writeErr(w, unauthorized())
		return tenant.Principal{}, false
	}
	return p, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := httpapi.NewRequestDecoder(w, r, 1<<20)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalid("invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalid("request body must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func writeBody(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func writeErr(w http.ResponseWriter, err error) {
	var he *Error
	if errors.As(err, &he) {
		writeBody(w, he.Status, he.Code, he.Message)
		return
	}
	switch {
	case errors.Is(err, ErrClosed):
		writeBody(w, http.StatusConflict, "conflict", "plugin installation is closed")
	case errors.Is(err, ErrDenied):
		writeBody(w, http.StatusForbidden, "forbidden", "plugin permission denied")
	case errors.Is(err, ErrLeaseHeld):
		writeBody(w, http.StatusConflict, "conflict", "plugin job lease is held")
	default:
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23503" {
			writeBody(w, http.StatusForbidden, "forbidden", "unknown principal")
			return
		}
		slog.Error("plugins", "err", err)
		writeBody(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
