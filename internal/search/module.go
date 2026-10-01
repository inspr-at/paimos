// SPDX-License-Identifier: AGPL-3.0-only

package search

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/embedding"
	"github.com/inspr-at/paimos/internal/httpapi"
)

// Module serves GET /api/search. New returns it as an httpapi.Module.
type Module struct {
	pool     *pgxpool.Pool
	provider embedding.Provider
	resolve  embedding.Resolver
}

// New returns the search module. A nil provider serves lexical search only.
func New(pool *pgxpool.Pool, provider embedding.Provider) httpapi.Module {
	return &Module{pool: pool, provider: provider}
}

// NewWithResolver shares the workspace provider choice with the indexing queue.
func NewWithResolver(pool *pgxpool.Pool, resolve embedding.Resolver) httpapi.Module {
	return &Module{pool: pool, resolve: resolve}
}

// Mount registers GET /api/search.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/search", m.handleSearch)
}
