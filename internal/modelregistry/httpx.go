// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, msg string) error { return &httpError{status: status, msg: msg} }

var (
	slugRE   = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	modelRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	effortRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	uuidRE   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return tenant.Principal{}, false
	}
	return p, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(http.StatusBadRequest, "invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fail(http.StatusBadRequest, "request body must contain one JSON value")
	}
	return nil
}

func writeErr(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		httpapi.WriteError(w, he.status, he.msg)
		return
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			httpapi.WriteError(w, http.StatusConflict, "profile version already exists")
			return
		case "23514", "23503":
			httpapi.WriteError(w, http.StatusBadRequest, "invalid model profile")
			return
		}
	}
	httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
}

func validHarness(s string) bool {
	switch s {
	case "codex", "claude", "pi", "cursor", "grok":
		return true
	default:
		return false
	}
}

func validFamily(s string) bool {
	switch s {
	case "openai", "anthropic", "xai", "cursor":
		return true
	default:
		return false
	}
}

func validTier(s string) bool {
	switch s {
	case "fast", "standard", "strong", "frontier":
		return true
	default:
		return false
	}
}

func validRole(s string) bool {
	_, ok := roleByName(s)
	return ok
}
