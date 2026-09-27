// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reportercontract"
)

// Module serves the approval routes.
type Module struct {
	pool     *pgxpool.Pool
	inTenant func(context.Context, *pgxpool.Pool, string, func(pgx.Tx) error) error
}

var _ httpapi.Module = (*Module)(nil)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// New returns an httpapi.Module for the approval routes. Mount it from the
// coordinator; queries use db.InTenant.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool, inTenant: db.InTenant}
}

// Mount registers the approval routes on mux.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/approvals", reportercontract.WithHeader(reportercontract.Approvals, m.handleList))
	mux.HandleFunc("POST /api/approvals", reportercontract.WithHeader(reportercontract.Approvals, m.handlePropose))
	mux.HandleFunc("POST /api/approvals/{approvalId}/decision", reportercontract.WithHeader(reportercontract.Approvals, m.handleDecide))
	mux.HandleFunc("POST /api/approvals/{approvalId}/revoke", reportercontract.WithHeader(reportercontract.Approvals, m.handleRevoke))
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, msg string) error {
	return &httpError{status: status, msg: msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteError(w, status, msg)
}

func writeResult(w http.ResponseWriter, status int, v any, err error) {
	if err == nil {
		writeJSON(w, status, v)
		return
	}
	err = mapDB(err)
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.msg)
		return
	}
	slog.Error("approvals", "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

func mapDB(err error) error {
	var he *httpError
	if errors.As(err, &he) || errors.Is(err, errNoKey) {
		if errors.Is(err, errNoKey) {
			return fail(http.StatusForbidden, "scope exceeds the API key")
		}
		return err
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Code {
	case "23505":
		return fail(http.StatusConflict, "approval is already decided")
	case "23514":
		return fail(http.StatusBadRequest, "invalid approval")
	case "23503":
		return fail(http.StatusForbidden, "resource is not in this tenant")
	case "P0001":
		switch {
		case strings.Contains(pe.Message, "only an agent"):
			return fail(http.StatusForbidden, "only an agent may propose its own permission")
		case strings.Contains(pe.Message, "only a person"):
			return fail(http.StatusConflict, "approval has expired")
		case strings.Contains(pe.Message, "permission grant"):
			return fail(http.StatusConflict, "permission grant must match an approved request")
		}
	}
	return err
}
