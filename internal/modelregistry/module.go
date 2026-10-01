// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves /api/models.
type Module struct {
	pool      *pgxpool.Pool
	vaultKey  []byte
	discovery *http.Client
}

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for /api/models. The coordinator mounts it.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool}
}

// Mount registers model registry routes.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/models/proposals/accept", m.acceptProposal)
	mux.HandleFunc("GET /api/models/refresh", m.refreshStatus)
	mux.HandleFunc("POST /api/models/refresh", m.refresh)
	mux.HandleFunc("POST /api/models/reports", m.reports)
	mux.HandleFunc("PUT /api/models/refresh/settings", m.putSettings)
	mux.HandleFunc("PUT /api/models/refresh/credentials/{accountId}", m.putCredential)
	mux.HandleFunc("GET /api/models", m.list)
	mux.HandleFunc("POST /api/models", m.create)
	mux.HandleFunc("PUT /api/models/routes", m.replace)
	mux.HandleFunc("GET /api/models/resolve", m.resolve)
}

func (m *Module) in(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, fn)
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var items []Profile
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := ensureCatalog(r.Context(), tx, p); err != nil {
			return err
		}
		var err error
		items, err = listProfiles(r.Context(), tx)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, items)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in profileWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out Profile
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = createProfile(r.Context(), tx, p, in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (m *Module) replace(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in []Route
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out []Route
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		out, err = replaceRoutes(r.Context(), tx, p, in, now)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) requirePermission(r *http.Request, p tenant.Principal, permission string) error {
	if p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), permission, authz.Scope{}) != nil {
		return fail(http.StatusForbidden, "permission denied")
	}
	return nil
}

func (m *Module) resolve(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := resolveQuery{
		Role:         r.URL.Query().Get("role"),
		AuthorFamily: r.URL.Query().Get("author_family"),
		Harness:      r.URL.Query().Get("harness"),
	}
	var out Resolution
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := ensureCatalog(r.Context(), tx, p); err != nil {
			return err
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		out, err = resolveRole(r.Context(), tx, q, now)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func dbNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now)
	return now, err
}
