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
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// Module serves /api/models.
type Module struct {
	pool            *pgxpool.Pool
	routesTimeout   time.Duration
	validationClock func(context.Context, pgx.Tx) (time.Time, error)
	vaultKey        []byte
	discovery       *http.Client
}

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for /api/models. The coordinator mounts it.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool}
}

// Mount registers model registry routes.
func (m *Module) Mount(mux *http.ServeMux) {
	m.mount(mux, m.legacyPreferencesReadOnly)
}

// The retained legacy writer is exercised directly by compatibility tests;
// the public module mounts only the read-only retirement response.
func (m *Module) mount(mux *http.ServeMux, legacy http.HandlerFunc) {
	mux.HandleFunc("GET /api/model-preferences/simple", boundedPreferenceHandler(m.simple))
	mux.HandleFunc("PUT /api/models/lines/{harness}/{model}", boundedPreferenceHandler(m.editLine))
	mux.HandleFunc("GET /api/models/lines/{harness}/{model}/usage", boundedPreferenceHandler(m.lineUsage))
	mux.HandleFunc("GET /api/model-preferences/board", boundedPreferenceHandler(m.board))
	mux.HandleFunc("PUT /api/model-preferences/orders/{column}/{situation}", boundedPreferenceHandler(m.writeBoardOrder))
	mux.HandleFunc("DELETE /api/model-preferences/orders/{column}/{situation}", boundedPreferenceHandler(m.writeBoardOrder))
	mux.HandleFunc("PUT /api/model-preferences/orders/{column}/{situation}/thinking", boundedPreferenceHandler(m.writeBoardOrder))
	mux.HandleFunc("PUT /api/model-preferences/profile", boundedPreferenceHandler(m.writeBoardProfile))
	mux.HandleFunc("POST /api/model-preferences/tray/{line}/dismiss", boundedPreferenceHandler(m.dismissBoardLine))
	mux.HandleFunc("GET /api/model-preferences/situations", boundedPreferenceHandler(m.situationLimits))
	mux.HandleFunc("PUT /api/model-preferences/situations", boundedPreferenceHandler(m.writeSituationLimits))
	mux.HandleFunc("GET /api/model-preferences/evidence", boundedPreferenceHandler(m.boardEvidence))
	mux.HandleFunc("GET /api/model-preferences/coverage", boundedPreferenceHandler(m.boardCoverage))
	mux.HandleFunc("GET /api/model-rules", boundedPreferenceHandler(m.modelRules))
	mux.HandleFunc("PUT /api/model-rules/{scope}/{column}", boundedPreferenceHandler(m.writeModelRules))
	mux.HandleFunc("PUT /api/work-kinds/order", boundedPreferenceHandler(m.orderWorkKinds))

	mux.HandleFunc("GET /api/settings/lead-policy", boundedPreferenceHandler(m.leadSettings))
	mux.HandleFunc("PUT /api/settings/lead-policy", boundedPreferenceHandler(m.writeLeadSettings))
	mux.HandleFunc("DELETE /api/settings/lead-policy", boundedPreferenceHandler(m.writeLeadSettings))
	mux.HandleFunc("GET /api/projects/{projectId}/lead-settings", boundedPreferenceHandler(m.leadSettings))
	mux.HandleFunc("PUT /api/projects/{projectId}/lead-settings", boundedPreferenceHandler(m.writeLeadSettings))
	mux.HandleFunc("DELETE /api/projects/{projectId}/lead-settings", boundedPreferenceHandler(m.writeLeadSettings))
	mux.HandleFunc("GET /api/model-preferences", boundedPreferenceHandler(m.preferences))
	mux.HandleFunc("PUT /api/model-preferences/levels/{level}", boundedPreferenceHandler(legacy))
	mux.HandleFunc("DELETE /api/model-preferences/levels/{level}", boundedPreferenceHandler(legacy))
	mux.HandleFunc("PUT /api/model-preferences/levels/{level}/rows/{kindId}", boundedPreferenceHandler(legacy))
	mux.HandleFunc("DELETE /api/model-preferences/levels/{level}/rows/{kindId}", boundedPreferenceHandler(legacy))
	mux.HandleFunc("GET /api/work-kinds", boundedPreferenceHandler(m.listWorkKinds))
	mux.HandleFunc("POST /api/work-kinds", boundedPreferenceHandler(m.writeWorkKind))
	mux.HandleFunc("PATCH /api/work-kinds/{kindId}", boundedPreferenceHandler(m.writeWorkKind))
	mux.HandleFunc("DELETE /api/work-kinds/{kindId}", boundedPreferenceHandler(m.writeWorkKind))
	mux.HandleFunc("POST /api/work-kinds/{kindId}/restore", boundedPreferenceHandler(m.writeWorkKind))
	mux.HandleFunc("POST /api/models/{id}/retire", boundedPreferenceHandler(m.retirement))
	mux.HandleFunc("DELETE /api/models/{id}/retire", boundedPreferenceHandler(m.retirement))
	mux.HandleFunc("POST /api/models/proposals/accept", m.acceptProposal)
	mux.HandleFunc("GET /api/models/refresh", m.refreshStatus)
	mux.HandleFunc("POST /api/models/refresh", m.refresh)
	mux.HandleFunc("POST /api/models/reports", m.reports)
	mux.HandleFunc("PUT /api/models/refresh/settings", m.putSettings)
	mux.HandleFunc("PUT /api/models/refresh/credentials/{accountId}", m.putCredential)
	mux.HandleFunc("GET /api/models", m.list)
	mux.HandleFunc("POST /api/models", m.create)
	mux.HandleFunc("PUT /api/models/routes", m.replace)
	mux.HandleFunc("GET /api/models/routes", m.readRoutes)
	mux.HandleFunc("GET /api/models/resolve", m.resolve)
}

func (m *Module) in(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true),set_config('statement_timeout','5s',true)`); err != nil {
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	var items []Profile
	if err := PrepareCatalog(ctx, m.pool, p, CatalogPreparation{Operation: CatalogRead, Request: r}); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		current, err := workorders.CurrentKeyPrincipal(r, tx, p, "models.read")
		if err != nil {
			return err
		}
		if err = authz.RequireTx(r.Context(), tx, current, "models.read", authz.Scope{}); err != nil {
			return err
		}

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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in profileWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	in = normalizeProfileWrite(in)
	if err := validateProfile(in); err != nil {
		writeErr(w, err)
		return
	}
	var out Profile
	if err := PrepareCatalog(ctx, m.pool, p, CatalogPreparation{Operation: CatalogManage, Request: r}); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		current, err := workorders.CurrentKeyPrincipal(r, tx, p, "models.manage")
		if err != nil {
			return err
		}
		if err = authz.RequireTx(r.Context(), tx, current, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		if err := catalogLock(r.Context(), tx); err != nil {
			return err
		}

		out, err = createProfile(r.Context(), tx, p, in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (m *Module) requirePermission(r *http.Request, p tenant.Principal, permission string) error {
	if p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), permission, authz.Scope{}) != nil {
		return fail(http.StatusForbidden, "permission denied")
	}
	return nil
}

func (m *Module) resolve(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	if params.Has("mode") {
		if params.Get("mode") != "placement" {
			writeErr(w, fail(http.StatusBadRequest, "invalid resolution mode"))
			return
		}
		boundedPreferenceHandler(m.resolvePreferences)(w, r)
		return
	}
	// project_id predates placement resolution: it adds residency evidence to
	// the CLI ladder without changing review ordering or account requirements.
	for _, name := range []string{"ticket", "area", "complexity", "person_id", "situation", "fix_round", "estimate_hours", "concept", "previous_family"} {
		if r.URL.Query().Has(name) {
			boundedPreferenceHandler(m.resolvePreferences)(w, r)
			return
		}
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	q := resolveQuery{
		Role:         r.URL.Query().Get("role"),
		AuthorFamily: r.URL.Query().Get("author_family"),
		Harness:      r.URL.Query().Get("harness"),
	}
	var err error
	q, err = validateResolveQuery(q)
	if err != nil {
		writeErr(w, err)
		return
	}
	project := r.URL.Query().Get("project_id")
	if project != "" && !uuidRE.MatchString(project) {
		writeErr(w, fail(http.StatusBadRequest, "invalid project_id"))
		return
	}
	var out struct {
		Resolution
		Trace PreferenceTrace `json:"trace"`
	}
	if err := PrepareCatalog(ctx, m.pool, p, CatalogPreparation{Operation: CatalogRead, Request: r, Authorize: func(ctx context.Context, tx pgx.Tx, current tenant.Principal) (bool, error) {
		if project != "" {
			if err := authz.RequireTx(ctx, tx, current, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
				return false, err
			}
		}
		return true, nil
	}}); err != nil {
		writeErr(w, err)
		return
	}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		current, err := workorders.CurrentKeyPrincipal(r, tx, p, "models.read")
		if err != nil {
			return err
		}
		if err = authz.RequireTx(r.Context(), tx, current, "models.read", authz.Scope{}); err != nil {
			return err
		}

		if err := workorders.RefreshPrincipal(r.Context(), tx, current); err != nil {
			return err
		}
		if project != "" {
			if err := db.LockTree(r.Context(), tx, p.TenantID); err != nil {
				return err
			}
			if err := authz.RequireTx(r.Context(), tx, current, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
				return err
			}
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}

		query := WorkQuery{Role: q.Role, AuthorFamily: q.AuthorFamily, Harness: q.Harness, ProjectID: project, PersonID: modelprefs.PrefsPerson(r.Context(), tx, current)}
		resolved, err := resolveDailyWith(r.Context(), tx, current, query, now, func(query WorkQuery) (WorkResolution, error) {
			resolution, err := resolveRole(r.Context(), tx, resolveQuery{Role: query.Role, AuthorFamily: query.AuthorFamily, Harness: query.Harness, ProjectID: project, OffHarnesses: query.OffHarnesses}, now)
			if err != nil {
				return WorkResolution{}, err
			}
			_, trace, _, err := placementTrace(r.Context(), tx, query)
			return WorkResolution{Resolution: resolution, Trace: trace, Residency: trace.Residency.Value}, err
		})
		out.Resolution, out.Trace = resolved.Resolution, resolved.Trace
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
