// SPDX-License-Identifier: AGPL-3.0-only
package accountuse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Module { return &Module{pool} }

type endpoint func(*http.Request, pgx.Tx, tenant.Principal) (any, error)

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account-use", m.handler(m.read, false))
	mux.HandleFunc("PUT /api/account-use/rules", m.handler(m.rules, true))
	mux.HandleFunc("PATCH /api/account-use/cells", m.handler(m.cells, true))
	mux.HandleFunc("POST /api/account-use/confirm", m.handler(m.confirm, true))
	mux.HandleFunc("POST /api/work-contexts", m.handler(m.createContext, true))
	mux.HandleFunc("PATCH /api/work-contexts/{contextId}", m.handler(m.updateContext, true))
	mux.HandleFunc("GET /api/projects/{projectId}/work-context", m.handler(m.readProject, false))
	mux.HandleFunc("PUT /api/projects/{projectId}/work-context", m.handler(m.project, true))
}
func (m *Module) handler(fn endpoint, write bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok {
			httpapi.WriteError(w, 401, "authentication required")
			return
		}
		if p.Kind != tenant.Person {
			httpapi.WriteError(w, 403, "person account.use.manage required")
			return
		}
		if write {
			if err := httpapi.BufferRequestBody(w, r, 256<<10); err != nil {
				httpapi.WriteError(w, 413, "account-use body exceeds bound")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		var out any
		err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','4s',true),set_config('statement_timeout','4s',true)`); err != nil {
				return err
			}
			if write {
				// Lifecycle and project writes must enter tree before matrix.
				if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
					return err
				}
			} else {
				if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
					return err
				}
				if err := LockShared(ctx, tx); err != nil {
					return err
				}
				if err := authz.RequireTx(ctx, tx, p, Permission, authz.Scope{}); err != nil {
					return err
				}
			}
			var err error
			out, err = fn(r, tx, p)
			return err
		})
		if err != nil {
			writeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, out)
	}
}
func decode(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fail(400, "invalid account-use body")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fail(400, "one JSON body required")
	}
	return nil
}
func writeError(w http.ResponseWriter, err error) {
	var e *Error
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &e):
		httpapi.WriteError(w, e.Status, e.Message)
	case errors.Is(err, authz.ErrForbidden):
		httpapi.WriteError(w, 403, "person account.use.manage required")
	case errors.As(err, &pg) && (pg.Code == "23514" || pg.Code == "23503"):
		httpapi.WriteError(w, 400, "invalid account-use target")
	case errors.As(err, &pg) && pg.Code == "23505":
		httpapi.WriteError(w, 409, "account-use target already exists")
	default:
		httpapi.WriteError(w, 500, "account-use operation failed")
	}
}
func uuid(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func page(r *http.Request) (int, error) {
	if r.URL.Query().Get("limit") == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 200 {
		return 0, fail(400, "limit must be 1 through 200")
	}
	return n, nil
}
