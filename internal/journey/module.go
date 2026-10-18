// SPDX-License-Identifier: AGPL-3.0-only

// Package journey retains only retired API compatibility routes. It never reads
// or writes the stored Flow; release membership is owned by releases.
package journey

import (
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
)

type Module struct{}

func New(_ *pgxpool.Pool) httpapi.Module { return &Module{} }

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/journey/next-actions", httpapi.RetiredFlow)
	mux.HandleFunc("GET /api/projects/{projectId}/journey", reportercontract.WithHeader(reportercontract.Journey, httpapi.RetiredFlow))
	mux.HandleFunc("PUT /api/projects/{projectId}/journey/profile", httpapi.RetiredFlow)
	mux.HandleFunc("POST /api/projects/{projectId}/journey/actions", httpapi.RetiredFlow)
}
