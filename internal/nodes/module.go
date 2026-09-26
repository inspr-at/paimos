// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves /api/kinds, /api/nodes and atomic /api/tags mutations.
type Module struct {
	pool   *pgxpool.Pool
	events Writer
}

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for kinds, nodes, tag assignment and
// GET /api/tickets/graph. The coordinator mounts it; this package does not
// edit cmd/aeon or register a plugin manifest. events records each mutation
// inside the tenant transaction. The ticket graph is read-only and writes no
// event. A nil events value selects SQLWriter until internal/events exposes
// its writer.
func New(pool *pgxpool.Pool, events Writer) httpapi.Module {
	if events == nil {
		events = SQLWriter{}
	}
	return &Module{pool: pool, events: events}
}

// Mount registers kind and node routes. Paths are the full /api paths the
// server mux expects.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/kinds", m.handleListKinds)
	mux.HandleFunc("POST /api/kinds", m.requirePermission("kinds.manage", m.handleCreateKind))
	mux.HandleFunc("GET /api/kinds/{kindId}", m.handleGetKind)
	mux.HandleFunc("PATCH /api/kinds/{kindId}", m.requirePermission("kinds.manage", m.handleUpdateKind))
	mux.HandleFunc("DELETE /api/kinds/{kindId}", m.requirePermission("kinds.manage", m.handleDeleteKind))

	mux.HandleFunc("GET /api/nodes", m.handleListNodes)
	mux.HandleFunc("GET /api/projects", m.handleListProjects)
	mux.HandleFunc("POST /api/nodes", m.handleCreateNode)
	mux.HandleFunc("POST /api/nodes/bulk", m.handleBulk)
	mux.HandleFunc("GET /api/nodes/lookup", m.handleLookupNodes)
	mux.HandleFunc("GET /api/nodes/tree", m.handleTree)
	mux.HandleFunc("GET /api/node-keys/{key}", m.handleGetNodeByKey)
	mux.HandleFunc("GET /api/nodes/{nodeId}", m.handleGetNode)
	mux.HandleFunc("PATCH /api/nodes/{nodeId}", m.handleUpdateNode)
	mux.HandleFunc("DELETE /api/nodes/{nodeId}", m.handleDeleteNode)
	mux.HandleFunc("POST /api/nodes/{nodeId}/move", m.handleMoveNode)
	mux.HandleFunc("POST /api/nodes/{nodeId}/project-move", m.handleProjectMove)
	mux.HandleFunc("GET /api/tickets/graph", m.handleTicketGraph)
	mux.HandleFunc("PATCH /api/tags/{tagId}", m.handleUpdateTag)
	mux.HandleFunc("DELETE /api/tags/{tagId}", m.requirePermission("tags.manage", m.handleDeleteTag))
}

func (m *Module) requirePermission(permission string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		if p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), permission, authz.Scope{}) != nil {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		next(w, r)
	}
}

func (m *Module) tx(ctx context.Context, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		return fn(ctx, tx)
	})
}

// lockTree serializes tree edits for this tenant on the same advisory key the
// node trigger uses, so position assignment and cycle checks cannot race.
func lockTree(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id', true), 0))`)
	return err
}
