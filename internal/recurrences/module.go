// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	mux.HandleFunc("GET /api/recurrences/{recurrenceId}", m.get)
	mux.HandleFunc("PUT /api/recurrences/{recurrenceId}", m.update)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/pause", m.pause)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/resume", m.resume)
	mux.HandleFunc("POST /api/recurrences/{recurrenceId}/run-now", m.runNow)
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
	return nil
}

// Match project-membership and owner-workstation writers: tenant access fence,
// tenant tree, node/record rows, then event counter. A non-key fence does not
// block tenant FK share locks. The worker uses try-locks to yield to foreground work.
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
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, tenantID).Scan(&got); err != nil || !got {
			return false, err
		}
	} else if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID); err != nil {
		return false, err
	}
	return true, nil
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
		template, _ := json.Marshal(in.Template)
		trigger, _ := json.Marshal(in.Trigger)
		out, err = scanRecurrence(tx.QueryRow(r.Context(), `INSERT INTO recurrences(tenant_id,project_id,parent_id,template,trigger,queue_each,overlap_policy,catch_up_policy,next_at,event_cursor,created_by_principal_id,created_at,updated_at,active_since)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,(SELECT coalesce(max(id),0) FROM events WHERE type='release.published'),$10,$11,$11,$11) RETURNING `+recurrenceColumns, p.TenantID, in.ProjectID, in.ParentID, template, trigger, in.QueueEach, in.OverlapPolicy, in.CatchUpPolicy, next, p.ID, now))
		if err != nil {
			return err
		}
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
		return manage(r.Context(), tx, p, out.ProjectID)
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
	if project != "" && !workorders.UUID(project) || after != "" && !workorders.UUID(after) {
		httpapi.WriteError(w, 400, "invalid project or cursor")
		return
	}
	items := []Recurrence{}
	var next *string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, Permission, authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		check, err := authz.ProjectsTx(r.Context(), tx, p)
		if err != nil {
			return err
		}
		// Bound the SQL result in the projects whose permission was evaluated;
		// pagination must not skip visible work behind unmanaged rows.
		allowed, err := authz.GrantedProjectIDsTx(r.Context(), tx, p, Permission)
		if err != nil {
			return err
		}
		workspace := check(Permission, "")
		rows, err := tx.Query(r.Context(), `SELECT `+recurrenceColumns+` FROM recurrences WHERE ($1='' OR project_id=nullif($1,'')::uuid) AND ($2='' OR id>nullif($2,'')::uuid) AND ($3 OR project_id=ANY($4::uuid[])) ORDER BY id LIMIT 101`, project, after, workspace, allowed)
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
		return rows.Err()
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
		if err = manage(r.Context(), tx, p, before.ProjectID); err != nil {
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
		if comparable == before.Trigger {
			in.Trigger = before.Trigger
		}
		if err = in.Input.normalize(now); err != nil {
			return workorders.Fail(400, err.Error())
		}
		if in.ProjectID != before.ProjectID || in.ParentID != before.ParentID {
			return workorders.Fail(400, "project and parent are immutable")
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
		if in.Trigger != before.Trigger {
			active = now
			next, err = nextTime(in.Trigger, now)
			if err != nil {
				return err
			}
			if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(id),0) FROM events WHERE type='release.published'`).Scan(&cursor); err != nil {
				return err
			}
		}
		template, _ := json.Marshal(in.Template)
		trigger, _ := json.Marshal(in.Trigger)
		out, err = scanRecurrence(tx.QueryRow(r.Context(), `UPDATE recurrences SET template=$2,trigger=$3,queue_each=$4,overlap_policy=$5,catch_up_policy=$6,next_at=$7,event_cursor=$8,revision=revision+1,updated_at=$9,active_since=$10 WHERE id=$1 RETURNING `+recurrenceColumns, before.ID, template, trigger, in.QueueEach, in.OverlapPolicy, in.CatchUpPolicy, next, cursor, now, active))
		if err != nil {
			return err
		}
		return record(r.Context(), tx, p, out.ProjectID, "recurrence.updated", before, out)
	})
	reply(w, 200, out, err)
}
func (m *Module) pause(w http.ResponseWriter, r *http.Request)  { m.setPaused(w, r, true) }
func (m *Module) resume(w http.ResponseWriter, r *http.Request) { m.setPaused(w, r, false) }
func (m *Module) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
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
		if err = manage(r.Context(), tx, p, before.ProjectID); err != nil {
			return err
		}
		if in.Revision < 1 || in.Revision != before.Revision {
			return workorders.Fail(409, "recurrence revision changed")
		}
		out = before
		if before.Paused == paused {
			return nil
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		next := before.NextAt
		cursor := before.EventCursor
		active := before.ActiveSince
		if !paused {
			active = now
			next, err = nextTime(before.Trigger, now)
			if err != nil {
				return err
			}
			if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(id),0) FROM events WHERE type='release.published'`).Scan(&cursor); err != nil {
				return err
			}
		}
		out, err = scanRecurrence(tx.QueryRow(r.Context(), `UPDATE recurrences SET paused=$2,next_at=$3,event_cursor=$4,revision=revision+1,updated_at=$5,active_since=$6 WHERE id=$1 RETURNING `+recurrenceColumns, before.ID, paused, next, cursor, now, active))
		if err != nil {
			return err
		}
		typ := "recurrence.resumed"
		if paused {
			typ = "recurrence.paused"
		}
		return record(r.Context(), tx, p, out.ProjectID, typ, before, out)
	})
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
		if err = manage(r.Context(), tx, p, item.ProjectID); err != nil {
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
	meta, _ := json.Marshal(map[string]string{"job": Job})
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: typ, Before: before, After: after, Metadata: meta})
	return err
}
