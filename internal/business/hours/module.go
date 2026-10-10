// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Writer interface {
	Append(context.Context, pgx.Tx, tenant.Principal, events.Change) (events.Event, error)
}
type Module struct {
	pool     *pgxpool.Pool
	registry *plugins.Registry
	writer   Writer
}

var _ httpapi.Module = (*Module)(nil)

// New exposes all hours routes using the coordinator's shared compiled registry.
// An optional event writer supports atomicity testing; nil uses events.Writer.
func New(pool *pgxpool.Pool, registry *plugins.Registry, writer ...Writer) *Module {
	m := &Module{pool: pool, registry: registry, writer: events.Writer{}}
	if len(writer) > 0 && writer[0] != nil {
		m.writer = writer[0]
	}
	return m
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /api/time-entries/{id}", m.mutateEntry)
	mux.HandleFunc("DELETE /api/time-entries/{id}", m.mutateEntry)
	for _, route := range []struct {
		pattern, operation string
		status             int
		fn                 func(*http.Request, pgx.Tx, tenant.Principal) (any, error)
	}{
		{"GET /api/time-periods", "", 200, m.listPeriods},
		{"POST /api/time-periods", "time_entry", 201, m.createPeriod},
		{"GET /api/time-periods/{periodId}", "", 200, m.getPeriod},
		{"POST /api/time-periods/{periodId}/approve", "period_approve", 200, m.approve},
		{"GET /api/time-entries", "", 200, m.listEntries},
		{"POST /api/time-entries", "time_entry", 201, m.createEntry},
		{"GET /api/nodes/{nodeId}/time-totals", "", 200, m.totals},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			scope := "hours.read"
			if route.operation != "" {
				scope = "hours.write"
			}
			if route.operation == "period_approve" {
				scope = "hours.approve"
			}
			workorders.Endpoint(m.pool, scope, false, route.status, func(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
				p.ID = strings.ToLower(p.ID)
				p.TenantID = strings.ToLower(p.TenantID)
				if authz.RequireTx(r.Context(), tx, p, scope, authz.Scope{}) != nil {
					return nil, fail(403, "permission denied")
				}
				for _, key := range []string{"periodId", "nodeId"} {
					if v := r.PathValue(key); v != "" && !workorders.UUID(v) {
						return nil, fail(400, "invalid id")
					}
				}
				if err := m.gate(r.Context(), tx, p, route.operation); err != nil {
					return nil, err
				}
				v, err := route.fn(r, tx, p)
				if period, ok := v.(Period); ok && err == nil && period.digest != "" {
					w.Header().Set("X-Entries-SHA256", period.digest)
				}
				if entry, ok := v.(Entry); ok && err == nil {
					w.Header().Set("ETag", entryTag(entry))
				}
				if page, ok := v.(listPage[Entry]); ok {
					if page.next != "" {
						w.Header().Set("X-Next-Cursor", page.next)
					}
					v = page.items
				}
				if page, ok := v.(listPage[Period]); ok {
					if page.next != "" {
						w.Header().Set("X-Next-Cursor", page.next)
					}
					v = page.items
				}
				return v, err
			})(w, r)
		})
	}
}

func fail(status int, message string) error { return workorders.Fail(status, message) }

func admin(ctx context.Context, tx pgx.Tx, p tenant.Principal) bool {
	return p.Kind == tenant.Person && authz.RequireTx(ctx, tx, p, "hours.approve", authz.Scope{}) == nil
}
func (m *Module) gate(ctx context.Context, tx pgx.Tx, p tenant.Principal, operation string) error {
	if m.registry == nil {
		return fail(403, "hours plugins unavailable")
	}
	// Hold installations against disable/re-pin through commit. Stable lock order
	// is costs, hours, then period; approval rechecks after its contested row lock.
	for _, id := range []string{"business_costs", PluginID} {
		var pinned string
		if err := tx.QueryRow(ctx, `SELECT plugin_id FROM plugin_installations WHERE tenant_id=$1 AND plugin_id=$2 FOR SHARE`, p.TenantID, id).Scan(&pinned); err == pgx.ErrNoRows {
			return fail(403, "required business plugin is disabled")
		} else if err != nil {
			return err
		}
	}
	// Reading hourly prices needs the cost view, not authority to change rates.
	if err := m.viewEnabled(ctx, tx, p.TenantID, "business_costs"); err != nil {
		return err
	}
	if operation != "" {
		enabled, err := plugins.Enabled(ctx, tx, m.registry, p.TenantID, PluginID, operation)
		if err != nil {
			return err
		}
		if !enabled {
			return fail(403, "hours plugin unavailable or under-granted")
		}
	}
	if operation != "" {
		return nil
	}
	return m.viewEnabled(ctx, tx, p.TenantID, PluginID)
}
func (m *Module) viewEnabled(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	plug, ok := m.registry.Lookup(id)
	if !ok || !slices.Contains(plug.Manifest.Permissions, fence.PermViewsProvide) {
		return fail(403, "business view unavailable")
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT enabled AND manifest_digest_sha256=$3 AND $4=ANY(permissions) FROM plugin_installations WHERE tenant_id=$1 AND plugin_id=$2`, tenantID, id, plug.Manifest.DigestSHA256, fence.PermViewsProvide).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return fail(403, "business view unavailable or under-granted")
	}
	return nil
}
func filter(r *http.Request, key string) (string, error) {
	v := strings.ToLower(r.URL.Query().Get(key))
	if v != "" && !workorders.UUID(v) {
		return "", fail(400, "invalid "+key)
	}
	return v, nil
}
