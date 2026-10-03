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
	pool          *pgxpool.Pool
	routesTimeout time.Duration
}

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for /api/models. The coordinator mounts it.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool}
}

// Mount registers model registry routes.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/model-preferences", boundedPreferenceHandler(m.preferences))
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
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id',true),0))`); err != nil {
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

func (m *Module) replace(w http.ResponseWriter, r *http.Request) {
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
	var in []Route
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var err error
	in, err = normalizeRouteStructure(in)
	if err != nil {
		writeErr(w, err)
		return
	}
	var out []Route
	if err := PrepareCatalog(ctx, m.pool, p, CatalogPreparation{Operation: CatalogManage, Request: r}); err != nil {
		writeErr(w, err)
		return
	}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
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
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id',true),0))`); err != nil {
			return err
		}

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
	for _, name := range []string{"ticket", "area", "complexity", "project_id", "person_id"} {
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
		out.Resolution, err = resolveRole(r.Context(), tx, q, now)
		if err != nil {
			return err
		}
		// Preserve this CLI-facing role ladder. Qualified review dispatch uses
		// ResolveReviewFor; attach the same residency evidence additively here.
		_, out.Trace, _, err = placementTrace(r.Context(), tx, WorkQuery{
			Role: q.Role, PersonID: modelprefs.PrefsPerson(r.Context(), tx, current), ProjectID: project,
		})
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
