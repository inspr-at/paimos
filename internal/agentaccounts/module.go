// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves /api/agent-accounts.
type Module struct {
	pool    *pgxpool.Pool
	preview *previewGuard
}

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for /api/agent-accounts. The coordinator mounts it.
// Settle and Release are the in-process ledger operations for the run package;
// they are not HTTP routes.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool}
}

// Mount registers account, allowance and routing routes.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/agent-accounts/{accountId}/signals", m.signals)
	mux.HandleFunc("GET /api/agent-accounts/{accountId}/statusline", m.statusline)
	mux.HandleFunc("PUT /api/agent-accounts/{accountId}/statusline", m.statusline)
	mux.HandleFunc("POST /api/agent-accounts/{accountId}/quota-key", m.quotaKey)
	mux.HandleFunc("GET /api/agent-accounts", m.list)
	mux.HandleFunc("GET /api/agent-accounts/catalog", m.catalog)
	mux.HandleFunc("GET /api/agent-accounts/capacity", m.capacityList)
	mux.HandleFunc("POST /api/agent-accounts/{accountId}/capacity/approve", m.approveCapacity)
	mux.HandleFunc("POST /api/agent-accounts/capacity/preview", m.capacityPreview)
	mux.HandleFunc("GET /api/agent-accounts/capacity/schedule", m.capacitySchedule)
	mux.HandleFunc("PUT /api/agent-accounts/capacity/schedule", m.capacitySchedule)
	mux.HandleFunc("POST /api/agent-accounts/{accountId}/readings", m.ingestReadings)
	mux.HandleFunc("GET /api/agent-accounts/{accountId}/readings", m.capacityHistory)
	mux.HandleFunc("PUT /api/agent-accounts/{accountId}/metadata", m.metadata)
	mux.HandleFunc("POST /api/agent-accounts", m.register)
	mux.HandleFunc("POST /api/agent-accounts/route", m.route)
	mux.HandleFunc("POST /api/agent-accounts/{accountId}/windows", m.createWindow)
	mux.HandleFunc("PATCH /api/agent-accounts/{accountId}", m.patch)
	mux.HandleFunc("POST /api/agent-accounts/{accountId}/probe", m.probe)
}

func (m *Module) in(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := authz.Require(authz.BindPool(r.Context(), m.pool), "account.read", authz.Scope{}); err != nil {
		writeErr(w, fail(http.StatusForbidden, "permission denied"))
		return
	}
	var items []Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		items, err = listAccounts(r.Context(), tx)
		if p.Kind == tenant.Agent {
			own := []Account{}
			for _, a := range items {
				if a.RegisteredBy == p.ID {
					own = append(own, a)
				}
			}
			items = own
		}
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, items)
}

func (m *Module) register(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requireAgent(p); err != nil {
		writeErr(w, err)
		return
	}
	var in accountWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := requireScope(r.Context(), tx, r, p, "account.manage"); err != nil {
			return err
		}
		var err error
		out, err = registerAccount(r.Context(), tx, p, in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (m *Module) createWindow(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in windowWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out Window
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = createWindow(r.Context(), tx, p, r.PathValue("accountId"), in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (m *Module) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in stateWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = updateState(r.Context(), tx, p, r.PathValue("accountId"), in.State)
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

func (m *Module) probe(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requireAgent(p); err != nil {
		writeErr(w, err)
		return
	}
	var in probeWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = reportProbe(r.Context(), tx, p, r.PathValue("accountId"), in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) route(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		RunID          string           `json:"run_id"`
		DaemonID       string           `json:"daemon_id"`
		AccountIDs     []string         `json:"account_ids"`
		EstimatedUnits map[string]int64 `json:"estimated_units"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, err)
		return
	}
	var out RouteResult
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = reserve(r.Context(), tx, r, p, body.RunID, body.DaemonID, body.AccountIDs, body.EstimatedUnits)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
