// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	pool *pgxpool.Pool
	// now is an injected database-clock substitute for deterministic tests only.
	now     func(context.Context, pgx.Tx) (time.Time, error)
	history []Publication
}

func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool} }
func (m *Module) clock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	if m.now != nil {
		return m.now(ctx, tx)
	}
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), err
}

// WithHistory supplies immutable published product releases from the binary's
// existing release history. ProjectKey confines them to that owning project.
func (m *Module) WithHistory(publications []Publication) *Module {
	m.history = append([]Publication(nil), publications...)
	return m
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/recurrences", m.list)
	mux.HandleFunc("POST /api/recurrences", m.create)
	mux.HandleFunc("POST /api/recurrences/preview", m.previewDraft)
	mux.HandleFunc("GET /api/recurrences/{recurrenceId}", m.get)
	mux.HandleFunc("PUT /api/recurrences/{recurrenceId}", m.update)
	mux.HandleFunc("DELETE /api/recurrences/{recurrenceId}", m.remove)
	mux.HandleFunc("GET /api/recurrences/{recurrenceId}/history", m.listHistory)
	mux.HandleFunc("GET /api/recurrences/{recurrenceId}/releases", m.listReleases)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/pause", m.pause)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/resume", m.resume)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/run-now", m.runNow)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/events", m.receiveExternal)
	mux.HandleFunc("GET /api/recurrences/{recurrenceId}/preview", m.preview)
}
func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "unauthorized")
		return p, false
	}
	if id := r.PathValue("recurrenceId"); id != "" && !workorders.UUID(id) {
		httpapi.WriteError(w, 400, "invalid recurrence id")
		return p, false
	}
	return p, true
}
func decode(w http.ResponseWriter, r *http.Request, in any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(in); err != nil {
		httpapi.WriteError(w, 400, "invalid recurrence request")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		httpapi.WriteError(w, 400, "one JSON object required")
		return false
	}
	return true
}
func reply(w http.ResponseWriter, status int, out any, err error) {
	if err != nil {
		workorders.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, status, out)
}

// Management always checks the live binding inside the writing transaction.
// Agent grants are explicit custom-role permissions, never implicit owner/admin
// inheritance (authz.readGrants enforces that rule for every route).
func manage(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	return authz.RequireTx(ctx, tx, p, Permission, authz.Scope{ProjectID: project})
}
func readPermission(p tenant.Principal) string {
	if p.Kind == tenant.Person {
		return "nodes.read"
	}
	return Permission
}
func read(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	return authz.RequireTx(ctx, tx, p, readPermission(p), authz.Scope{ProjectID: project})
}
func authorizeDefinition(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Input) error {
	for _, perm := range []string{Permission, "nodes.write"} {
		if err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: in.ProjectID}); err != nil {
			return err
		}
	}
	if in.QueueEach {
		for _, perm := range []string{"run.create", "work_orders.write"} {
			if err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: in.ProjectID}); err != nil {
				return err
			}
		}
	}
	if err := authorizeSources(ctx, tx, p, in); err != nil {
		return err
	}
	return validateDefinition(ctx, tx, p, in)
}

// Tenant access fence precedes tree and resource rows. Try mode yields rather
// than waiting behind either foreground fence; FK share locks remain compatible.
func lock(ctx context.Context, tx pgx.Tx, tenantID string, try bool) (bool, error) {
	var id string
	query := `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`
	if try {
		query += ` SKIP LOCKED`
	}
	err := tx.QueryRow(ctx, query, tenantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && try {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if try {
		var got bool
		err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, tenantID).Scan(&got)
		return got, err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID)
	return err == nil, err
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in Input
	if !decode(w, r, &in) {
		return
	}
	var out Recurrence
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(r.Context(), tx, p.TenantID, false); err != nil {
			return err
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		if err = in.normalize(now); err != nil {
			return workorders.Fail(400, err.Error())
		}
		if err = authorizeDefinition(r.Context(), tx, p, in); err != nil {
			return err
		}
		if err = validateTarget(r.Context(), tx, in, true); err != nil {
			return err
		}
		next, err := nextTime(in.Trigger, now)
		if err != nil {
			return err
		}
		var id string
		if err = tx.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		if err = saveDefinition(r.Context(), tx, p, id, in); err != nil {
			return err
		}
		var scope any
		if in.Definition != nil {
			scope = in.Definition.Scope.Kind
		}
		template, _ := json.Marshal(in.Template)
		trigger, _ := json.Marshal(in.Trigger)
		out, err = scanRecurrence(tx.QueryRow(r.Context(), `INSERT INTO recurrences(tenant_id,project_id,parent_id,template,trigger,queue_each,overlap_policy,catch_up_policy,next_at,event_cursor,created_by_principal_id,created_at,updated_at,active_since,id,definition_scope,paused)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,(SELECT coalesce(max(id),0) FROM events WHERE ($5::jsonb->>'kind'='event' AND $5::jsonb->>'event'<>'release.published') OR type='release.published'),$10,$11,$11,$11,$12,$13,$13::text IS NOT NULL) RETURNING `+recurrenceColumns, p.TenantID, in.ProjectID, in.ParentID, template, trigger, in.QueueEach, in.OverlapPolicy, in.CatchUpPolicy, next, p.ID, now, id, scope))
		if err != nil {
			return err
		}
		out.Definition = in.Definition
		return record(r.Context(), tx, p, out.ProjectID, "recurrence.created", nil, out)
	})
	reply(w, 201, out, err)
}
func nextTime(t Trigger, now time.Time) (*time.Time, error) {
	if t.Kind == "event" {
		return nil, nil
	}
	s, err := parseSchedule(t)
	if err != nil {
		return nil, err
	}
	at, err := s.next(now)
	return &at, err
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var out Recurrence
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = readDefinition(r.Context(), tx, p, out.Input); err != nil {
			return err
		}
		return results(r.Context(), tx, []Recurrence{out}, func(items []Recurrence) { out = items[0] })
	})
	reply(w, 200, out, err)
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.URL.Query().Get("project_id")
	after := r.URL.Query().Get("after")
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != "project" && scope != "personal" && scope != "workspace" {
		httpapi.WriteError(w, 400, "invalid definition scope")
		return
	}
	if project != "" && !workorders.UUID(project) || after != "" && !workorders.UUID(after) {
		httpapi.WriteError(w, 400, "invalid project or cursor")
		return
	}
	items := []Recurrence{}
	var next *string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		permission := readPermission(p)
		if err := authz.RequireTx(r.Context(), tx, p, permission, authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		check, err := authz.ProjectsTx(r.Context(), tx, p)
		if err != nil {
			return err
		}
		// Bound the SQL result in the projects whose permission was evaluated;
		// pagination must not skip visible work behind unmanaged rows.
		allowed, err := authz.GrantedProjectIDsTx(r.Context(), tx, p, permission)
		if err != nil {
			return err
		}
		workspace := check(permission, "")
		rows, err := tx.Query(r.Context(), `SELECT `+recurrenceColumns+` FROM recurrences WHERE ($1='' OR project_id=nullif($1,'')::uuid) AND ($2='' OR id>nullif($2,'')::uuid) AND ($3 OR project_id=ANY($4::uuid[])) AND (definition_scope IS NULL OR EXISTS(SELECT 1 FROM recurrence_definitions d WHERE d.recurrence_id=recurrences.id AND d.tenant_id=recurrences.tenant_id AND ((d.scope_type='personal' AND $6) OR (d.scope_type='workspace' AND $3) OR (d.scope_type='project' AND ($3 OR d.scope_project_id=ANY($4::uuid[])))))) AND ($5='' OR coalesce(definition_scope,'project')=$5) AND retired_at IS NULL ORDER BY id LIMIT 101`, project, after, workspace, allowed, scope, p.Kind == tenant.Person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanRecurrence(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		if len(items) > 100 {
			id := items[99].ID
			next = &id
			items = items[:100]
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if err := definitions(r.Context(), tx, items, func(out []Recurrence) { items = out }); err != nil {
			return err
		}
		return results(r.Context(), tx, items, func(out []Recurrence) { items = out })
	})
	reply(w, 200, map[string]any{"items": items, "next_cursor": next}, err)
}
func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Input
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	var out Recurrence
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(r.Context(), tx, p.TenantID, false); err != nil {
			return err
		}
		before, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = manageDefinition(r.Context(), tx, p, before.Input); err != nil {
			return err
		}
		if in.ExpectedRevision < 1 || in.ExpectedRevision != before.Revision {
			return workorders.Fail(409, "recurrence revision changed")
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		comparable := in.Trigger
		if comparable.StartDate == "" {
			comparable.StartDate = before.Trigger.StartDate
		}
		if reflect.DeepEqual(comparable, before.Trigger) {
			in.Trigger = before.Trigger
		}
		if in.Definition == nil {
			in.Definition = before.Definition
		}
		if err = in.Input.normalize(now); err != nil {
			return workorders.Fail(400, err.Error())
		}
		if in.ProjectID != before.ProjectID || in.ParentID != before.ParentID {
			return workorders.Fail(400, "project and parent are immutable")
		}
		// Legacy routines and their audit events are shared with project readers.
		// Conversion must preserve that visibility boundary.
		if before.Definition == nil && in.Definition != nil && (in.Definition.Scope.Kind != "project" || in.Definition.Scope.ProjectID != before.ProjectID) {
			return workorders.Fail(400, "legacy definitions must retain their project scope; create a new scoped definition")
		}
		if before.Definition != nil && (in.Definition.Scope != before.Definition.Scope || in.Definition.OwnerPrincipalID != before.Definition.OwnerPrincipalID) {
			return workorders.Fail(400, "definition scope and owner are immutable")
		}
		if err = authorizeDefinition(r.Context(), tx, p, in.Input); err != nil {
			return err
		}
		if err = validateTarget(r.Context(), tx, in.Input, true); err != nil {
			return err
		}
		next := before.NextAt
		cursor := before.EventCursor
		active := before.ActiveSince
		if !reflect.DeepEqual(in.Trigger, before.Trigger) {
			active = now
			next, err = nextTime(in.Trigger, now)
			if err != nil {
				return err
			}
			if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(id),0) FROM events WHERE ($1='event' AND $2<>'release.published') OR type='release.published'`, in.Trigger.Kind, in.Trigger.Event).Scan(&cursor); err != nil {
				return err
			}
		}
		if err = saveDefinition(r.Context(), tx, p, before.ID, in.Input); err != nil {
			return err
		}
		var scope any
		if in.Definition != nil {
			scope = in.Definition.Scope.Kind
		}
		template, _ := json.Marshal(in.Template)
		trigger, _ := json.Marshal(in.Trigger)
		out, err = scanRecurrence(tx.QueryRow(r.Context(), `UPDATE recurrences SET template=$2,trigger=$3,queue_each=$4,overlap_policy=$5,catch_up_policy=$6,next_at=$7,event_cursor=$8,revision=revision+1,updated_at=$9,active_since=$10,paused=paused OR (definition_scope IS NULL AND $11::text IS NOT NULL),definition_scope=$11 WHERE id=$1 RETURNING `+recurrenceColumns, before.ID, template, trigger, in.QueueEach, in.OverlapPolicy, in.CatchUpPolicy, next, cursor, now, active, scope))
		if err != nil {
			return err
		}
		out.Definition = in.Definition
		return record(r.Context(), tx, p, out.ProjectID, "recurrence.updated", before, out)
	})
	reply(w, 200, out, err)
}
func (m *Module) pause(w http.ResponseWriter, r *http.Request)  { m.setPaused(w, r, true, false) }
func (m *Module) resume(w http.ResponseWriter, r *http.Request) { m.setPaused(w, r, false, false) }

// All lifecycle changes share the same tree/access fence and live authorization.
// Retirement must still write its tombstone when the recurrence is already paused.
func (m *Module) setPaused(w http.ResponseWriter, r *http.Request, paused, retire bool) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Revision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	var out Recurrence
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(r.Context(), tx, p.TenantID, false); err != nil {
			return err
		}
		before, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = manageDefinition(r.Context(), tx, p, before.Input); err != nil {
			return err
		}
		if in.Revision < 1 || in.Revision != before.Revision {
			return workorders.Fail(409, "recurrence revision changed")
		}
		out = before
		if !retire && before.Paused == paused {
			return nil
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		if retire {
			out, err = scanRecurrence(tx.QueryRow(r.Context(), `UPDATE recurrences SET paused=true,retired_at=$2,revision=revision+1,updated_at=$2 WHERE id=$1 RETURNING `+recurrenceColumns, before.ID, now))
			if err != nil {
				return err
			}
			out.Definition = before.Definition
			return record(r.Context(), tx, p, before.ProjectID, "recurrence.deleted", before, out)
		}
		next := before.NextAt
		cursor := before.EventCursor
		active := before.ActiveSince
		if !paused {
			if before.Definition != nil {
				if err = authorizeDefinition(r.Context(), tx, p, before.Input); err != nil {
					return err
				}
			}
			active = now
			next, err = nextTime(before.Trigger, now)
			if err != nil {
				return err
			}
			if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(id),0) FROM events WHERE ($1='event' AND $2<>'release.published') OR type='release.published'`, before.Trigger.Kind, before.Trigger.Event).Scan(&cursor); err != nil {
				return err
			}
		}
		out, err = scanRecurrence(tx.QueryRow(r.Context(), `UPDATE recurrences SET paused=$2,next_at=$3,event_cursor=$4,revision=revision+1,updated_at=$5,active_since=$6 WHERE id=$1 RETURNING `+recurrenceColumns, before.ID, paused, next, cursor, now, active))
		if err != nil {
			return err
		}
		out.Definition = before.Definition
		typ := "recurrence.resumed"
		if paused {
			typ = "recurrence.paused"
		}
		return record(r.Context(), tx, p, out.ProjectID, typ, before, out)
	})
	if retire && err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	reply(w, 200, out, err)
}
func (m *Module) preview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	count := 5
	if raw := r.URL.Query().Get("count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			httpapi.WriteError(w, 400, "count must be 1..100")
			return
		}
		count = n
	}
	var times []time.Time
	var trigger string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = readDefinition(r.Context(), tx, p, item.Input); err != nil {
			return err
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		if raw := r.URL.Query().Get("after"); raw != "" {
			now, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return workorders.Fail(400, "after must be an RFC3339 timestamp")
			}
		}
		trigger = item.Trigger.Kind
		times, err = Preview(item.Trigger, now, count)
		return err
	})
	reply(w, 200, map[string]any{"times": times, "trigger_kind": trigger}, err)
}
func record(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, typ string, before, after any) error {
	id := ""
	if item, ok := after.(Recurrence); ok {
		id = item.ID
	}
	meta, _ := json.Marshal(map[string]string{"job": Job, "recurrence_id": id})
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: typ, Before: before, After: after, Metadata: meta})
	return err
}
