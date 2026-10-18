// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
)

type Module struct {
	pool      *pgxpool.Pool
	inTenant  func(context.Context, *pgxpool.Pool, string, func(pgx.Tx) error) error
	eventSink EventWriter
	now       func() time.Time
}

// New returns an httpapi.Module serving the saved view endpoints.
func New(pool *pgxpool.Pool) httpapi.Module {
	return NewWithEventWriter(pool, sqlEventWriter{})
}

// NewWithEventWriter returns a module using the provided transactional event writer.
func NewWithEventWriter(pool *pgxpool.Pool, writer EventWriter) httpapi.Module {
	if writer == nil {
		writer = sqlEventWriter{}
	}
	return &Module{pool: pool, inTenant: db.InTenant, eventSink: writer, now: time.Now}
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/views", m.list)
	mux.HandleFunc("GET /api/views/deleted", m.listDeleted)
	mux.HandleFunc("POST /api/views", m.create)
	mux.HandleFunc("GET /api/views/{viewId}", m.get)
	mux.HandleFunc("PATCH /api/views/{viewId}", m.patch)
	mux.HandleFunc("DELETE /api/views/{viewId}", m.delete)
	mux.HandleFunc("POST /api/views/{viewId}/restore", m.restore)
	mux.HandleFunc("GET /api/preferences/{key}", m.getPreference)
	mux.HandleFunc("PUT /api/preferences/{key}", m.putPreference)
	mux.HandleFunc("GET /api/agents/plan", m.getAgentsPlan)
}
