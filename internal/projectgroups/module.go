// SPDX-License-Identifier: AGPL-3.0-only

package projectgroups

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Event types. Each one is reversible through UndoHandlers.
const (
	EventCreated  = "project_group.created"
	EventUpdated  = "project_group.updated"
	EventDeleted  = "project_group.deleted"
	EventAssigned = "project_group.assigned"
)

const (
	maxNameRunes = 60
	maxProjects  = 500
	maxPosition  = 100_000
)

// Group is a shared project group with its live member projects.
type Group struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Position   int       `json:"position"`
	ProjectIDs []string  `json:"project_ids"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Assignment is one project's shared group; a nil GroupID means none.
type Assignment struct {
	ProjectID string  `json:"project_id"`
	GroupID   *string `json:"group_id"`
}

// snapshot is a group as its created and deleted events record it. Previous
// lists the shared groups the new group's projects came from, so undoing the
// creation can put them back.
type snapshot struct {
	Group
	Previous []Assignment `json:"previous,omitempty"`
}

// meta is what a rename or reorder changes.
type meta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type assignments struct {
	Assignments []Assignment `json:"assignments"`
}

// result answers a write: the group or assignments now, and the event to undo
// (null when nothing changed).
type result struct {
	Group       *Group       `json:"group,omitempty"`
	Assignments []Assignment `json:"assignments,omitempty"`
	EventID     *int64       `json:"event_id"`
}

// Module serves /api/project-groups.
type Module struct {
	pool *pgxpool.Pool
}

// New returns the shared project group module.
func New(pool *pgxpool.Pool) httpapi.Module { return &Module{pool: pool} }

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/project-groups", m.list)
	mux.HandleFunc("POST /api/project-groups", m.create)
	mux.HandleFunc("POST /api/project-groups/assign", m.assign)
	mux.HandleFunc("PATCH /api/project-groups/{groupId}", m.patch)
	mux.HandleFunc("DELETE /api/project-groups/{groupId}", m.remove)
}

// ---------- errors ----------

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(msg string) error { return &apiError{http.StatusBadRequest, msg} }
func notFound(msg string) error   { return &apiError{http.StatusNotFound, msg} }
func conflict(msg string) error   { return &apiError{http.StatusConflict, msg} }

var errNotAdmin = &apiError{http.StatusForbidden, "only workspace admins can change shared groups"}

func fail(w http.ResponseWriter, err error) {
	var ae *apiError
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &ae):
		httpapi.WriteError(w, ae.status, ae.msg)
	case errors.As(err, &pe) && pe.Code == "23505":
		httpapi.WriteError(w, http.StatusConflict, "a shared group with this name already exists")
	case errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01"):
		httpapi.WriteError(w, http.StatusConflict, "the groups changed at the same time; try again")
	default:
		httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
	}
}

// ---------- request helpers ----------

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return p, false
	}
	return p, true
}

func (m *Module) admin(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := principal(w, r)
	if !ok {
		return p, false
	}
	if authz.Require(authz.BindPool(r.Context(), m.pool), "project_groups.write", authz.Scope{}) != nil {
		fail(w, errNotAdmin)
		return p, false
	}
	return p, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := httpapi.NewRequestDecoder(w, r, 64<<10)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return badRequest("request body must contain one JSON value")
	}
	return nil
}

func validUUID(s string) bool {
	var u pgtype.UUID
	return len(s) == 36 && u.Scan(s) == nil && u.Valid
}

// cleanName trims a group name and checks it reads as one: 1 to 60 characters,
// no control characters.
func cleanName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", badRequest("name is required")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameRunes {
		return "", badRequest("name must be at most 60 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", badRequest("name must not contain control characters")
		}
	}
	return name, nil
}

// cleanProjects parses, lowercases and de-duplicates project ids.
func cleanProjects(raw []string) ([]string, error) {
	if len(raw) > maxProjects {
		return nil, badRequest("at most 500 projects at a time")
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, id := range raw {
		id = strings.ToLower(strings.TrimSpace(id))
		if !validUUID(id) {
			return nil, badRequest("project_ids must be UUIDs")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (m *Module) inTenant(ctx context.Context, p tenant.Principal, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, p.TenantID, fn)
}

// ---------- handlers ----------

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	items := make([]Group, 0)
	err := m.inTenant(r.Context(), p, func(tx pgx.Tx) error {
		var err error
		items, err = loadGroups(r.Context(), tx, "")
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, struct {
		Items []Group `json:"items"`
	}{items})
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       string   `json:"name"`
		ProjectIDs []string `json:"project_ids"`
		Position   *int     `json:"position"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	name, err := cleanName(in.Name)
	if err == nil {
		in.ProjectIDs, err = cleanProjects(in.ProjectIDs)
	}
	if err == nil && in.Position != nil && (*in.Position < 0 || *in.Position > maxPosition) {
		err = badRequest("position must be between 0 and 100000")
	}
	if err != nil {
		fail(w, err)
		return
	}
	var out result
	err = m.inTenant(r.Context(), p, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, "project_groups.write", authz.Scope{}) != nil {
			return errNotAdmin
		}
		if err := lockGroups(ctx, tx); err != nil {
			return err
		}
		if err := requireProjects(ctx, tx, in.ProjectIDs); err != nil {
			return err
		}
		if taken, err := nameTaken(ctx, tx, name, ""); err != nil || taken {
			if err == nil {
				err = conflict("a shared group with this name already exists")
			}
			return err
		}
		position := 0
		if in.Position != nil {
			position = *in.Position
		} else if err := tx.QueryRow(ctx, `SELECT coalesce(max(position) + 1, 0) FROM project_groups`).Scan(&position); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO project_groups (tenant_id, name, position, created_by)
			VALUES (current_setting('aeon.tenant_id')::uuid, $1, $2, $3::uuid)
			RETURNING id::text`, name, position, p.ID).Scan(&id); err != nil {
			return err
		}
		previous, err := currentAssignments(ctx, tx, in.ProjectIDs)
		if err != nil {
			return err
		}
		if err := setMembers(ctx, tx, in.ProjectIDs, &id); err != nil {
			return err
		}
		group, err := loadGroup(ctx, tx, id)
		if err != nil {
			return err
		}
		taken := make([]Assignment, 0)
		for _, a := range previous {
			if a.GroupID != nil {
				taken = append(taken, a)
			}
		}
		e, err := events.Append(ctx, tx, p, events.Change{Type: EventCreated, After: snapshot{Group: group, Previous: taken}})
		if err != nil {
			return err
		}
		out = result{Group: &group, EventID: &e.ID}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (m *Module) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("groupId"))
	if !validUUID(id) {
		fail(w, notFound("group not found"))
		return
	}
	var in struct {
		Name     *string `json:"name"`
		Position *int    `json:"position"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.Name == nil && in.Position == nil {
		fail(w, badRequest("patch is empty"))
		return
	}
	var name string
	if in.Name != nil {
		var err error
		if name, err = cleanName(*in.Name); err != nil {
			fail(w, err)
			return
		}
	}
	if in.Position != nil && (*in.Position < 0 || *in.Position > maxPosition) {
		fail(w, badRequest("position must be between 0 and 100000"))
		return
	}
	var out result
	err := m.inTenant(r.Context(), p, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, "project_groups.write", authz.Scope{}) != nil {
			return errNotAdmin
		}
		if err := lockGroups(ctx, tx); err != nil {
			return err
		}
		before, err := loadMeta(ctx, tx, id)
		if err != nil {
			return err
		}
		after := before
		if in.Name != nil {
			after.Name = name
		}
		if in.Position != nil {
			after.Position = *in.Position
		}
		if after != before {
			if !strings.EqualFold(after.Name, before.Name) {
				if taken, err := nameTaken(ctx, tx, after.Name, id); err != nil || taken {
					if err == nil {
						err = conflict("a shared group with this name already exists")
					}
					return err
				}
			}
			if err := writeMeta(ctx, tx, after); err != nil {
				return err
			}
			e, err := events.Append(ctx, tx, p, events.Change{Type: EventUpdated, Before: before, After: after})
			if err != nil {
				return err
			}
			out.EventID = &e.ID
		}
		group, err := loadGroup(ctx, tx, id)
		if err != nil {
			return err
		}
		out.Group = &group
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("groupId"))
	if !validUUID(id) {
		fail(w, notFound("group not found"))
		return
	}
	var out result
	err := m.inTenant(r.Context(), p, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, "project_groups.write", authz.Scope{}) != nil {
			return errNotAdmin
		}
		if err := lockGroups(ctx, tx); err != nil {
			return err
		}
		group, err := loadGroup(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_groups WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		e, err := events.Append(ctx, tx, p, events.Change{Type: EventDeleted, Before: snapshot{Group: group}})
		if err != nil {
			return err
		}
		out.EventID = &e.ID
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) assign(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		GroupID    *string  `json:"group_id"`
		ProjectIDs []string `json:"project_ids"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	ids, err := cleanProjects(in.ProjectIDs)
	if err == nil && len(ids) == 0 {
		err = badRequest("project_ids is required")
	}
	if err == nil && in.GroupID != nil {
		lower := strings.ToLower(*in.GroupID)
		in.GroupID = &lower
		if !validUUID(lower) {
			err = notFound("group not found")
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	out := result{Assignments: []Assignment{}}
	err = m.inTenant(r.Context(), p, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, "project_groups.write", authz.Scope{}) != nil {
			return errNotAdmin
		}
		if err := lockGroups(ctx, tx); err != nil {
			return err
		}
		if in.GroupID != nil {
			if _, err := loadMeta(ctx, tx, *in.GroupID); err != nil {
				return err
			}
		}
		if err := requireProjects(ctx, tx, ids); err != nil {
			return err
		}
		current, err := currentAssignments(ctx, tx, ids)
		if err != nil {
			return err
		}
		var before, after []Assignment
		var changed []string
		for _, a := range current {
			if sameGroup(a.GroupID, in.GroupID) {
				continue
			}
			before = append(before, a)
			after = append(after, Assignment{ProjectID: a.ProjectID, GroupID: in.GroupID})
			changed = append(changed, a.ProjectID)
		}
		if len(changed) == 0 {
			return nil
		}
		if err := setMembers(ctx, tx, changed, in.GroupID); err != nil {
			return err
		}
		change := events.Change{Type: EventAssigned, Before: assignments{before}, After: assignments{after}}
		if len(changed) == 1 {
			change.NodeID = &changed[0]
		}
		e, err := events.Append(ctx, tx, p, change)
		if err != nil {
			return err
		}
		out = result{Assignments: after, EventID: &e.ID}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func sameGroup(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ---------- storage ----------

// lockGroups serializes shared group writes (and their undo) in the tenant. The
// events module takes the event counter last, so this order cannot deadlock.
func lockGroups(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('project_groups:' || current_setting('aeon.tenant_id'), 0))`)
	return err
}

const groupSelect = `
	SELECT g.id::text, g.name, g.position, g.created_by::text, g.created_at, g.updated_at,
	       coalesce(array_agg(m.project_id::text ORDER BY m.project_id) FILTER (WHERE n.id IS NOT NULL), '{}')
	FROM project_groups g
	LEFT JOIN project_group_members m ON m.tenant_id = g.tenant_id AND m.group_id = g.id
	LEFT JOIN nodes n ON n.tenant_id = m.tenant_id AND n.id = m.project_id AND n.deleted_at IS NULL`

func loadGroups(ctx context.Context, tx pgx.Tx, id string) ([]Group, error) {
	query := groupSelect + ` WHERE ($1 = '' OR g.id = nullif($1, '')::uuid) GROUP BY g.tenant_id, g.id ORDER BY g.position, lower(g.name), g.id`
	rows, err := tx.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Group, 0)
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Position, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt, &g.ProjectIDs); err != nil {
			return nil, err
		}
		if g.ProjectIDs == nil {
			g.ProjectIDs = []string{}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func loadGroup(ctx context.Context, tx pgx.Tx, id string) (Group, error) {
	groups, err := loadGroups(ctx, tx, id)
	if err != nil {
		return Group{}, err
	}
	if len(groups) == 0 {
		return Group{}, notFound("group not found")
	}
	return groups[0], nil
}

func loadMeta(ctx context.Context, tx pgx.Tx, id string) (meta, error) {
	var out meta
	err := tx.QueryRow(ctx, `SELECT id::text, name, position FROM project_groups WHERE id = $1::uuid FOR UPDATE`, id).Scan(&out.ID, &out.Name, &out.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, notFound("group not found")
	}
	return out, err
}

func writeMeta(ctx context.Context, tx pgx.Tx, next meta) error {
	_, err := tx.Exec(ctx, `UPDATE project_groups SET name = $2, position = $3, updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond') WHERE id = $1::uuid`, next.ID, next.Name, next.Position)
	return err
}

func nameTaken(ctx context.Context, tx pgx.Tx, name, except string) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_groups WHERE lower(name) = lower($1) AND ($2 = '' OR id <> nullif($2, '')::uuid))`, name, except).Scan(&taken)
	return taken, err
}

// requireProjects checks every id is a live project in the tenant.
func requireProjects(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var found int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = ANY($1::uuid[]) AND n.deleted_at IS NULL AND k.slug = 'project'`, ids).Scan(&found); err != nil {
		return err
	}
	if found != len(ids) {
		return badRequest("project_ids must name live projects")
	}
	return nil
}

// liveProjects keeps the ids that still name live projects.
func liveProjects(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = ANY($1::uuid[]) AND n.deleted_at IS NULL AND k.slug = 'project'`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// currentAssignments reads each project's shared group, in the order of ids.
func currentAssignments(ctx context.Context, tx pgx.Tx, ids []string) ([]Assignment, error) {
	groups := map[string]string{}
	if len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT project_id::text, group_id::text FROM project_group_members WHERE project_id = ANY($1::uuid[]) FOR UPDATE`, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var project, group string
			if err := rows.Scan(&project, &group); err != nil {
				rows.Close()
				return nil, err
			}
			groups[project] = group
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]Assignment, 0, len(ids))
	for _, id := range ids {
		a := Assignment{ProjectID: id}
		if g, ok := groups[id]; ok {
			a.GroupID = &g
		}
		out = append(out, a)
	}
	return out, nil
}

// setMembers puts the projects into group, or out of every shared group when nil.
func setMembers(ctx context.Context, tx pgx.Tx, ids []string, group *string) error {
	if len(ids) == 0 {
		return nil
	}
	if group == nil {
		_, err := tx.Exec(ctx, `DELETE FROM project_group_members WHERE project_id = ANY($1::uuid[])`, ids)
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO project_group_members (tenant_id, project_id, group_id)
		SELECT current_setting('aeon.tenant_id')::uuid, id, $2::uuid FROM unnest($1::uuid[]) AS id
		ON CONFLICT (tenant_id, project_id) DO UPDATE SET group_id = EXCLUDED.group_id, added_at = now()`, ids, *group)
	return err
}
