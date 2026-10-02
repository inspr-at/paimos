// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/workorders"
)

// Module serves the usage dashboard.
type Module struct{ pool *pgxpool.Pool }

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for GET /api/usage/dashboard.
func New(pool *pgxpool.Pool) httpapi.Module { return &Module{pool: pool} }

// Mount registers the dashboard read.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/usage/model-estimates", workorders.Endpoint(m.pool, "harness.read", false, http.StatusOK, m.modelEstimates))
	mux.HandleFunc("GET /api/usage/dashboard", workorders.Endpoint(m.pool, "harness.read", false, http.StatusOK, m.dashboard))
}
