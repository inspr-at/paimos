// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// WithCausalUndoHandlers installs handlers available only through a previewed
// derived-parent action, leaving ordinary event Undo availability unchanged.
func WithCausalUndoHandlers(handlers map[string]UndoFunc) Option {
	return func(m *module) {
		for typ, fn := range handlers {
			m.causalUndo[typ] = fn
		}
	}
}

type module struct {
	pool       *pgxpool.Pool
	undo       map[string]UndoFunc
	causalUndo map[string]UndoFunc
}

// New returns a module for event history, SSE and registered resource undo.
func New(pool *pgxpool.Pool, options ...Option) httpapi.Module {
	m := &module{pool: pool, undo: make(map[string]UndoFunc), causalUndo: make(map[string]UndoFunc)}
	for _, option := range options {
		option(m)
	}
	return m
}

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/events", m.list)
	mux.HandleFunc("GET /api/events/activity", m.activity)
	mux.HandleFunc("GET /api/events/stream", m.stream)
	mux.HandleFunc("POST /api/events/{eventId}/undo", m.handleUndo)
	mux.HandleFunc("GET /api/events/{eventId}/undo-preview", m.handleUndoPreview)
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
	Items      []Event `json:"items"`
	NextAfter  *int64  `json:"next_after"`
	NextCursor *string `json:"next_cursor,omitempty"`
}

type eventRange struct {
	from, to *time.Time
	types    []string
	byTime   bool
	cursorAt *time.Time
	cursorID int64
	project  string
}

func (m *module) read(ctx context.Context, p tenant.Principal, node string, after int64, limit int, ranges ...eventRange) (page, error) {
	result := page{Items: make([]Event, 0)}
	var bounds eventRange
	if len(ranges) > 0 {
		bounds = ranges[0]
	}
	// Read as the reader: row-level security shows its projects only.
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Quote events use the quote-scoped collaboration stream, which rechecks
		// resource access and plugin installation. Never expose them on the
		// tenant-wide list or stream, even when node_id is supplied.
		query := `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of
   FROM events WHERE tenant_id=$1 AND id>$2 AND ($3::uuid IS NULL OR node_id=$3) AND type NOT LIKE 'quote.%'
     AND ($5::timestamptz IS NULL OR at >= $5) AND ($6::timestamptz IS NULL OR at < $6)
     AND ($7::text[] IS NULL OR type = ANY($7))`
		args := []any{p.TenantID, after, nullable(node), limit + 1, bounds.from, bounds.to, bounds.types}
		if bounds.project != "" {
			args = append(args, bounds.project)
			query += ` AND EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=events.tenant_id AND n.id=events.node_id AND n.project_id=$8::uuid)`
		}
		if bounds.byTime {
			// Direct bounds remain index conditions even for a generic prepared plan.
			query = strings.Replace(query, "($5::timestamptz IS NULL OR at >= $5) AND ($6::timestamptz IS NULL OR at < $6)", "at >= $5::timestamptz AND at < $6::timestamptz", 1)
			if bounds.cursorAt != nil {
				args = append(args, bounds.cursorAt, bounds.cursorID)
				query += ` AND (at,id)>($` + strconv.Itoa(len(args)-1) + `::timestamptz,$` + strconv.Itoa(len(args)) + `::bigint)`
			}
			query += ` ORDER BY at,id LIMIT $4`
		} else {
			query += ` ORDER BY id LIMIT $4`
		}
		rows, err := tx.Query(ctx, query, args...)
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
		if err := redactAccountEvents(ctx, tx, p, result.Items); err != nil {
			return err
		}
		return attachNodeChanges(ctx, tx, p.TenantID, result.Items)
	})
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		last := result.Items[limit-1].ID
		result.NextAfter = &last
		if bounds.byTime {
			at := result.Items[limit-1].At
			cursor := base64.RawURLEncoding.EncodeToString([]byte(at.Format(time.RFC3339Nano) + "|" + strconv.FormatInt(last, 10)))
			result.NextCursor = &cursor
		}
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
	var bounds eventRange
	if q.Has("briefing") || q.Has("since") {
		writeError(w, 400, "invalid_request", "briefing and since are no longer supported; use from/to")
		return
	}
	if q.Has("order") && q.Get("order") != "id" && q.Get("order") != "time" {
		writeError(w, 400, "invalid_request", "invalid order")
		return
	}
	bounds.byTime = q.Get("order") == "time" || q.Has("cursor")
	if q.Has("project_id") {
		if !validUUID(q.Get("project_id")) {
			writeError(w, 400, "invalid_request", "invalid project_id")
			return
		}
		bounds.project = q.Get("project_id")
	}
	if q.Has("from") || q.Has("to") {
		from, fromErr := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, toErr := time.Parse(time.RFC3339Nano, q.Get("to"))
		if fromErr != nil || toErr != nil || from.After(to) || to.Sub(from) > 366*24*time.Hour || from.Equal(to) && !bounds.byTime {
			writeError(w, 400, "invalid_request", "from and to must define an increasing RFC3339 range of at most 366 days")
			return
		}
		bounds.from, bounds.to = &from, &to
	}
	if bounds.byTime && (bounds.from == nil || q.Has("after")) {
		writeError(w, 400, "invalid_request", "time order requires from/to and excludes after")
		return
	}
	if q.Has("cursor") {
		decoded, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		parts := strings.Split(string(decoded), "|")
		if err != nil || len(q.Get("cursor")) > 256 || len(parts) != 2 {
			writeError(w, 400, "invalid_request", "invalid cursor")
			return
		}
		at, atErr := time.Parse(time.RFC3339Nano, parts[0])
		id, idErr := parseID(parts[1])
		if atErr != nil || idErr != nil || id < 1 {
			writeError(w, 400, "invalid_request", "invalid cursor")
			return
		}
		bounds.cursorAt, bounds.cursorID = &at, id
	}
	if q.Has("type") {
		bounds.types = strings.Split(q.Get("type"), ",")
		if len(bounds.types) > 8 {
			writeError(w, 400, "invalid_request", "at most eight event types may be requested")
			return
		}
		for _, typ := range bounds.types {
			if len(typ) == 0 || len(typ) > 128 || strings.TrimSpace(typ) != typ {
				writeError(w, 400, "invalid_request", "invalid event type")
				return
			}
		}
	}
	result, err := m.read(r.Context(), p, node, after, int(limit), bounds)
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
	// Network reads must finish before InTenant takes any mutation fences.
	// Empty bodies remain valid for ordinary Undo; derived Undo requires a
	// matching confirmation, rechecked against the locked current cause below.
	var confirm struct {
		Cause int64 `json:"confirmed_cause_event_id"`
	}
	if err := httpapi.BufferRequestBody(w, r, 1024); err != nil {
		failure(w, ErrConflict)
		return
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&confirm)
	if decodeErr != nil && decodeErr != io.EOF {
		failure(w, ErrConflict)
		return
	}
	if decodeErr == nil {
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			failure(w, ErrConflict)
			return
		}
	}
	var result Event
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Match the shared tenant -> pairing -> tree prefix before taking the
		// per-event lock. NO KEY UPDATE allows concurrent event FK shares.
		if _, err := tx.Exec(r.Context(), `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		// agentpairing depends on events, so use its shared SQL key directly.
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`); err != nil {
			return err
		}
		if err := authz.LockProjectWrite(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		// Reject callers with no Undo authority before looking up the event.
		// AnyProject is admission only: project-scoped causal grants still reach
		// the target checks below, under the same access-change fences.
		if err := authz.RequireTx(r.Context(), tx, p, "events.undo", authz.Scope{AnyProject: true}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return ErrForbidden
			}
			return err
		}
		e, err := scanEvent(tx.QueryRow(r.Context(), `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of
    FROM events WHERE tenant_id=$1 AND id=$2`, p.TenantID, id))
		if err != nil {
			return err
		}
		// Causal Undo resolves and checks every current project target in
		// causalChange under these same fences. A workspace-scope check here
		// would reject legitimate project grants before resolving hidden causes.
		if e.Type != derivedStatusEvent {
			if err := authz.RequireTx(r.Context(), tx, p, "events.undo", authz.RouteScope(r.Context())); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					return ErrForbidden
				}
				return err
			}
		}
		// Serialize attempts on this original event without modifying history or
		// taking the event counter before a resource lock (writers take it last).
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 12))`, p.TenantID+":"+strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		ownsActor := e.ActorPrincipalID == p.ID
		// Theme identity survives principal linking. Its resource undo handler
		// rechecks the current owner under the authority fence before writing.
		// This exception is limited to registered theme events, never agents or
		// unrelated workspace activity.
		if !ownsActor && p.Kind == tenant.Person && m.undo[e.Type] != nil && strings.HasPrefix(e.Type, "theme.") {
			if err := tx.QueryRow(r.Context(), `SELECT aeon_theme_owns($1::uuid)`, e.ActorPrincipalID).Scan(&ownsActor); err != nil {
				return err
			}
		}
		if e.Type != derivedStatusEvent && !ownsActor && authz.RequireTx(r.Context(), tx, p, "events.undo_other", authz.RouteScope(r.Context())) != nil {
			return ErrForbidden
		}
		fn := m.undo[e.Type]
		causeID := int64(0)
		if e.Type == derivedStatusEvent {
			cause, causeFn, preview, err := m.causalChange(r.Context(), tx, p, e)
			if err != nil {
				return err
			}
			if confirm.Cause != preview.CauseEventID {
				return ErrConflict
			}
			causeID = cause.ID
			fn = func(ctx context.Context, tx pgx.Tx, p tenant.Principal, _ Event) (Change, error) {
				return causeFn(ctx, tx, p, cause)
			}
		}
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
		if causeID != 0 {
			// Undo the original cause once, so sibling parent events cannot
			// reopen the same child a second time. Keep the parent action as
			// separate evidence without pretending its status was edited.
			change.UndoOf = &causeID
		} else {
			change.UndoOf = &id
		}
		result, err = Append(r.Context(), tx, p, change)
		if err != nil {
			return err
		}
		if causeID != 0 {
			metadata, err := json.Marshal(map[string]any{"cause_event_id": causeID, "rule_version": 1, "reason": "Reverted the triggering work change."})
			if err != nil {
				return err
			}
			if _, err := Append(r.Context(), tx, p, Change{NodeID: e.NodeID, Type: "status_autopilot.causal_undo", After: map[string]any{"id": *e.NodeID}, Metadata: metadata, UndoOf: &id}); err != nil {
				return err
			}
		}
		items := []Event{result}
		if err := redactAccountEvents(r.Context(), tx, p, items); err != nil {
			return err
		}
		result = items[0]
		return nil
	})
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, result)
}
