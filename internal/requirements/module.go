// SPDX-License-Identifier: AGPL-3.0-only
// Package requirements retains the retired Flow's requirement route compatibility.
package requirements

import (
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
)

type module struct{}

func New(_ *pgxpool.Pool) httpapi.Module { return &module{} }
func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{projectId}/requirements", httpapi.RetiredFlow)
	mux.HandleFunc("POST /api/projects/{projectId}/requirements", httpapi.RetiredFlow)
	mux.HandleFunc("POST /api/projects/{projectId}/requirements/agree", httpapi.RetiredFlow)
}
