// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoFunc restores a resource in tx and returns its actual before/after state.
// Implementations must lock, check staleness and enforce resource authorization.
type UndoFunc func(context.Context, pgx.Tx, tenant.Principal, Event) (Change, error)

type Option func(*module)

// WithUndo registers a reversible event type before the server starts.
func WithUndo(eventType string, fn UndoFunc) Option {
	return func(m *module) { m.undo[eventType] = fn }
}

// WithUndoHandlers registers a package's reversible event types.
func WithUndoHandlers(handlers map[string]UndoFunc) Option {
	return func(m *module) {
		for typ, fn := range handlers {
			m.undo[typ] = fn
		}
	}
}

type module struct {
	pool *pgxpool.Pool
	undo map[string]UndoFunc
}

// New returns a module for event history, SSE and registered resource undo.
func New(pool *pgxpool.Pool, options ...Option) httpapi.Module {
	m := &module{pool: pool, undo: make(map[string]UndoFunc)}
	for _, option := range options {
		option(m)
	}
	return m
}

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/events", m.list)
	mux.HandleFunc("GET /api/events/stream", m.stream)
	mux.HandleFunc("POST /api/events/{eventId}/undo", m.handleUndo)
}

// These errors let resource undo handlers return safe API outcomes.
var (
	ErrConflict  = errors.New("resource changed or event is not reversible")
	ErrForbidden = errors.New("undo is not permitted")
	ErrNotFound  = errors.New("not found")
)

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return p, false
	}
	return p, true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	httpapi.WriteJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

func failure(w http.ResponseWriter, err error) {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "not_found", "event or resource not found")
	case errors.Is(err, ErrForbidden), PortalCatalogDenied(err):
		writeError(w, 403, "forbidden", "undo is not permitted")
	case errors.Is(err, ErrConflict):
		writeError(w, 409, "conflict", ErrConflict.Error())
	case errors.As(err, &pe) && (strings.HasPrefix(pe.Code, "23") || pe.Code == "P0001" || pe.Code == "40001" || pe.Code == "40P01"):
		writeError(w, 409, "conflict", "change conflicts with current resource state")
	default:
		writeError(w, 500, "internal_error", "internal error")
	}
}

func parseID(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("missing id")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid id")
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

func validUUID(s string) bool {
	var u pgtype.UUID
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' && u.Scan(s) == nil && u.Valid
}

type page struct {
	Items     []Event `json:"items"`
	NextAfter *int64  `json:"next_after"`
}

func (m *module) read(ctx context.Context, p tenant.Principal, node string, after int64, limit int) (page, error) {
	result := page{Items: make([]Event, 0)}
	// Read as the reader: row-level security shows its projects only.
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Quote events use the quote-scoped collaboration stream, which rechecks
		// resource access and plugin installation. Never expose them on the
		// tenant-wide list or stream, even when node_id is supplied.
		rows, err := tx.Query(ctx, `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of
   FROM events WHERE tenant_id=$1 AND id>$2 AND ($3::uuid IS NULL OR node_id=$3) AND type NOT LIKE 'quote.%'
   ORDER BY id LIMIT $4`, p.TenantID, after, nullable(node), limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				rows.Close()
				return err
			}
			result.Items = append(result.Items, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return attachNodeChanges(ctx, tx, p.TenantID, result.Items)
	})
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		last := result.Items[limit-1].ID
		result.NextAfter = &last
	}
	return result, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	node := q.Get("node_id")
	after := int64(0)
	limit := int64(50)
	var err error
	if q.Has("after") {
		after, err = parseID(q.Get("after"))
	}
	if err == nil && q.Has("limit") {
		limit, err = parseID(q.Get("limit"))
	}
	if err != nil || limit < 1 || limit > 200 || (q.Has("node_id") && !validUUID(node)) {
		writeError(w, 400, "invalid_request", "invalid node_id, after or limit")
		return
	}
	result, err := m.read(r.Context(), p, node, after, int(limit))
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, result)
}

func (m *module) handleUndo(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("eventId"))
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_request", "invalid event ID")
		return
	}
	var result Event
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Serialize attempts on this original event without modifying history or
		// taking the event counter before a resource lock (writers take it last).
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 12))`, p.TenantID+":"+strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		e, err := scanEvent(tx.QueryRow(r.Context(), `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of
    FROM events WHERE tenant_id=$1 AND id=$2`, p.TenantID, id))
		if err != nil {
			return err
		}
		if e.ActorPrincipalID != p.ID && authz.RequireTx(r.Context(), tx, p, "events.undo_other", authz.RouteScope(r.Context())) != nil {
			return ErrForbidden
		}
		fn := m.undo[e.Type]
		if fn == nil || e.UndoOf != nil {
			return ErrConflict
		}
		var undone bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1 AND undo_of=$2)`, p.TenantID, id).Scan(&undone); err != nil {
			return err
		}
		if undone {
			return ErrConflict
		}
		change, err := fn(r.Context(), tx, p, e)
		if err != nil {
			return err
		}
		change.UndoOf = &id
		result, err = Append(r.Context(), tx, p, change)
		return err
	})
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, result)
}
