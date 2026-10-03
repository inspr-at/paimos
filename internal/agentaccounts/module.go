// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves /api/agent-accounts.
type Module struct {
	pool       *pgxpool.Pool
	preview    *previewGuard
	openRouter openrouter.Catalog
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
	handle := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, m.privateResponse(fn)) }
	handle("GET /api/agent-accounts/{accountId}/residency-evidence", m.residencyEvidence)
	handle("PUT /api/agent-accounts/{accountId}/residency-evidence", m.residencyEvidence)
	handle("GET /api/agent-accounts/readiness", m.readinessList)
	handle("POST /api/agent-accounts/{accountId}/check", m.check)
	handle("PUT /api/agent-accounts/{accountId}/sharing", m.sharing)
	handle("PUT /api/agent-accounts/quota-pool", m.quotaPool)
	handle("PUT /api/agent-accounts/{accountId}/signals", m.signals)
	handle("GET /api/agent-accounts/{accountId}/statusline", m.statusline)
	handle("PUT /api/agent-accounts/{accountId}/statusline", m.statusline)
	handle("POST /api/agent-accounts/{accountId}/quota-key", m.quotaKey)
	handle("GET /api/agent-accounts", m.list)
	handle("GET /api/agent-accounts/catalog", m.catalog)
	handle("GET /api/agent-accounts/groups", m.groups)
	handle("POST /api/agent-accounts/groups", m.groups)
	handle("PATCH /api/agent-accounts/groups/{id}", m.group)
	handle("DELETE /api/agent-accounts/groups/{id}", m.group)
	handle("GET /api/agent-accounts/pins", m.pins)
	handle("PUT /api/agent-accounts/pins", m.pins)
	handle("DELETE /api/agent-accounts/pins", m.pins)
	handle("GET /api/agent-accounts/use", m.use)
	handle("POST /api/agent-accounts/runs/{runId}/target", m.runTarget)
	handle("GET /api/agent-accounts/capacity", m.capacityList)
	handle("GET /api/agent-accounts/capacity/next", m.capacityNext)
	handle("POST /api/agent-accounts/{accountId}/capacity/approve", m.approveCapacity)
	handle("POST /api/agent-accounts/capacity/preview", m.capacityPreview)
	handle("GET /api/agent-accounts/capacity/schedule", m.capacitySchedule)
	handle("PUT /api/agent-accounts/capacity/schedule", m.capacitySchedule)
	handle("POST /api/agent-accounts/{accountId}/readings", m.ingestReadings)
	handle("GET /api/agent-accounts/{accountId}/readings", m.capacityHistory)
	handle("PUT /api/agent-accounts/{accountId}/metadata", m.metadata)
	handle("PUT /api/agent-accounts/{accountId}/label", m.rename)
	handle("PUT /api/agent-accounts/{accountId}/model", m.piModel)
	handle("POST /api/agent-accounts", m.register)
	handle("POST /api/agent-accounts/route", m.route)
	handle("POST /api/agent-accounts/{accountId}/windows", m.createWindow)
	handle("DELETE /api/agent-accounts/{accountId}/windows/{windowId}", m.removeWindow)
	handle("POST /api/agent-accounts/{accountId}/windows/{windowId}/repeat", m.repeatWindow)
	handle("PUT /api/agent-accounts/{accountId}/limit", m.putLimit)
	// A literal /limit here crosses groups/{id} at groups/limit, which Go's
	// ServeMux rejects. Keep the public URLs; the group route is more specific
	// than this fallback, whose only supported account resource is limit.
	handle("DELETE /api/agent-accounts/{accountId}/{resource}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("resource") != "limit" {
			http.NotFound(w, r)
			return
		}
		m.deleteLimit(w, r)
	})
	handle("PATCH /api/agent-accounts/{accountId}", m.patch)
	handle("POST /api/agent-accounts/{accountId}/archive", m.archive)
	handle("POST /api/agent-accounts/{accountId}/probe", m.probe)
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
		items, err = queryAccounts(r.Context(), tx, p.Kind == tenant.Agent)
		if err == nil && p.Kind == tenant.Person {
			err = annotateStatuslineOptIn(r.Context(), tx, p, items)
		}
		if p.Kind == tenant.Agent {
			own := []Account{}
			for _, a := range items {
				if a.RegisteredBy == p.ID {
					own = append(own, a)
				}
			}
			items = own
			if r.URL.Query().Get("include_checks") == "true" {
				if err := requireScope(r.Context(), tx, r, p, "account.probe"); err != nil {
					return err
				}
				for i := range items {
					a := &items[i]
					if err := agentpairing.AccountFence(r.Context(), tx, a.ID, false); err != nil {
						var fence *agentpairing.Error
						if errors.As(err, &fence) {
							continue
						}
						return err
					}
					now, err := dbNow(r.Context(), tx)
					if err != nil {
						return err
					}
					c, err := scanCheck(tx.QueryRow(r.Context(), `SELECT `+checkColumns+` FROM account_readiness_checks WHERE account_id=$1 AND binding_revision=$2 AND state='pending' AND requested_at>$3`, a.ID, a.LinkRevision, now.Add(-CheckTTL)))
					if err != nil && !isNoRows(err) {
						return err
					}
					if err == nil {
						a.PendingCheck = &c
					}
					a.ReadinessResources, err = ReadinessResources(r.Context(), tx, *a)
					if err != nil {
						return err
					}
				}
			}
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

// archive is Remove on an account row in /agents: person-only.
func (m *Module) archive(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var out Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = archiveAccount(r.Context(), tx, p, r.PathValue("accountId"))
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
	var completionErr *committedProbeError
	err := m.inReadinessWrite(r.Context(), p, func(tx pgx.Tx) error {
		scopes, err := keyScopes(r.Context(), tx, r, p)
		if err != nil {
			return err
		}
		reader := p
		reader.Scopes = scopes
		if !hasScope(scopes, "account.probe") && (in.Readiness != nil || !hasScope(scopes, "account.manage")) {
			return fail(403, "account probe scope required")
		}
		// Existing opaque enrollments historically authorize probes by their
		// live key scope and registering principal. Preserve that protocol;
		// the additive readiness report also requires current role authority.
		if in.Readiness != nil {
			if err := authz.RequireTx(r.Context(), tx, reader, "account.probe", authz.Scope{}); err != nil {
				return fail(403, "account probe permission required")
			}
		}
		var reportErr error
		out, reportErr = reportProbe(r.Context(), tx, p, r.PathValue("accountId"), in)
		if errors.As(reportErr, &completionErr) {
			return nil
		}
		return reportErr
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if completionErr != nil {
		w.Header().Set("X-Aeon-Write-Committed", "true")
		writeErr(w, completionErr.rejection)
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
	var routeErr error
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = reserve(r.Context(), tx, r, p, body.RunID, body.DaemonID, body.AccountIDs, body.EstimatedUnits)
		var released *routeCommitError
		if errors.As(err, &released) {
			routeErr = released.err
			return nil
		}
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if routeErr != nil {
		writeErr(w, routeErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
