// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"context"
	"net/http"
	"time"

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
	mux.HandleFunc("GET /api/projects/{projectId}/lead/usage", boundedLearning(workorders.Endpoint(m.pool, "harness.read", false, http.StatusOK, m.leadUsage)))
	mux.HandleFunc("GET /api/usage/model-estimates", boundedLearning(workorders.Endpoint(m.pool, "harness.read", false, http.StatusOK, m.modelEstimates)))
	mux.HandleFunc("GET /api/usage/dashboard", workorders.Endpoint(m.pool, "harness.read", false, http.StatusOK, m.dashboard))
}

// Bound the aggregate read independently of client/server connection timeouts.
func boundedLearning(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		handler(w, r.WithContext(ctx))
	}
}
