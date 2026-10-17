// SPDX-License-Identifier: AGPL-3.0-only

package costunits

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type httpError struct {
	status int
	code   string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error {
	return &httpError{status: http.StatusBadRequest, code: "invalid_request", msg: msg}
}

func forbidden(msg string) error {
	return &httpError{status: http.StatusForbidden, code: "forbidden", msg: msg}
}

func notFound(msg string) error {
	return &httpError{status: http.StatusNotFound, code: "not_found", msg: msg}
}

func conflict(msg string) error {
	return &httpError{status: http.StatusConflict, code: "conflict", msg: msg}
}

func unauthorized() error {
	return &httpError{status: http.StatusUnauthorized, code: "unauthorized", msg: "authentication required"}
}

func principalFrom(r *http.Request) (tenant.Principal, error) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		return tenant.Principal{}, unauthorized()
	}
	return p, nil
}

func validUUID(s string) bool { return uuidRe.MatchString(s) }

func validUnit(s string) bool {
	switch s {
	case "hour", "day", "item":
		return true
	default:
		return false
	}
}

func validCurrency(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := httpapi.NewRequestDecoder(w, r, 1<<20)
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return badRequest("invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return badRequest("request body must contain one JSON value")
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
	var he *httpError
	if errors.As(err, &he) {
		writeBody(w, he.status, he.code, he.msg)
		return
	}
	switch {
	case errors.Is(err, plugins.ErrClosed):
		writeBody(w, http.StatusConflict, "conflict", "plugin installation is closed")
	case errors.Is(err, plugins.ErrDenied):
		writeBody(w, http.StatusForbidden, "forbidden", "plugin permission denied")
	default:
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			switch pe.Code {
			case "23505":
				writeBody(w, http.StatusConflict, "conflict", "a rate already starts on that date")
				return
			case "23503":
				writeBody(w, http.StatusConflict, "conflict", "cost unit or principal is not in this tenant")
				return
			case "23514":
				writeBody(w, http.StatusBadRequest, "invalid_request", "invalid rate")
				return
			case "P0001":
				if strings.Contains(pe.Message, "historical") || strings.Contains(pe.Message, "closing") {
					writeBody(w, http.StatusConflict, "conflict", "cost unit rates are historical")
					return
				}
				if strings.Contains(pe.Message, "cost_unit") {
					writeBody(w, http.StatusNotFound, "not_found", "cost unit not found")
					return
				}
			}
		}
		slog.Error("costunits", "err", err)
		writeBody(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
