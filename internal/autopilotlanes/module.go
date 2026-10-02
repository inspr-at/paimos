// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool, now: time.Now} }
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{projectId}/autopilot-lanes", m.list)
	mux.HandleFunc("POST /api/projects/{projectId}/autopilot-lanes", m.create)
	mux.HandleFunc("GET /api/autopilot-lanes/{laneId}", m.get)
	mux.HandleFunc("PATCH /api/autopilot-lanes/{laneId}", m.patch)
	mux.HandleFunc("GET /api/autopilot-lanes/{laneId}/preview", m.preview)
	mux.HandleFunc("GET /api/autopilot-lanes/{laneId}/history", m.history)
	mux.HandleFunc("POST /api/autopilot-lanes/{laneId}/prepare", m.prepare)
	mux.HandleFunc("POST /api/autopilot-lanes/{laneId}/pause", m.pause)
	mux.HandleFunc("POST /api/autopilot-lanes/{laneId}/resume", m.resume)
}

type failure struct {
	status  int
	message string
}

func (e *failure) Error() string            { return e.message }
func fail(status int, message string) error { return &failure{status, message} }
func respond(w http.ResponseWriter, status int, out any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err == nil {
		httpapi.WriteJSON(w, status, out)
		return
	}
	var f *failure
	switch {
	case errors.As(err, &f):
		httpapi.WriteError(w, f.status, f.message)
	case errors.Is(err, authz.ErrForbidden):
		authz.WriteForbidden(w, err)
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "lane, project or ticket not found")
	default:
		slog.Error("autopilot lanes", "err", err)
		httpapi.WriteError(w, 500, "autopilot policy unavailable")
	}
}
func principal(w http.ResponseWriter, r *http.Request, person bool) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuid.MatchString(p.ID) || !uuid.MatchString(p.TenantID) {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	if p.Kind != tenant.Person && (person || p.Kind != tenant.Agent) {
		httpapi.WriteError(w, 403, "person required")
		return p, false
	}
	for _, key := range []string{"laneId", "projectId"} {
		if id := r.PathValue(key); id != "" && !uuid.MatchString(id) {
			httpapi.WriteError(w, 400, "invalid "+key)
			return p, false
		}
	}
	return p, true
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return fail(413, "body exceeds 16 KiB")
		}
		return fail(400, "invalid JSON object")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return fail(413, "body exceeds 16 KiB")
		}
		return fail(400, "one JSON object required")
	}
	return nil
}
func pageLimit(r *http.Request) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 100 {
		return 0, fail(400, "limit must be 1..100")
	}
	return n, nil
}
func (m *Module) transaction(r *http.Request, p tenant.Principal, write bool, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='10s'; SET LOCAL lock_timeout='5s'`); err != nil {
			return err
		}
		if write {
			if err := authz.LockProjectMutation(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		return fn(ctx, tx)
	})
}

func permission(ctx context.Context, tx pgx.Tx, p tenant.Principal, key, project string) error {
	return authz.RequireTx(ctx, tx, p, key, authz.Scope{ProjectID: project})
}
func project(ctx context.Context, tx pgx.Tx, id string) error {
	var value string
	return tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project'`, id).Scan(&value)
}
func scopeNode(ctx context.Context, tx pgx.Tx, s Scope, projectID string) error {
	if s.Kind != "release" {
		return nil
	}
	var id string
	return tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug='release'`, s.ReleaseNodeID, projectID).Scan(&id)
}

const columns = `l.node_id::text,l.project_id::text,l.owner_principal_id::text,l.name,l.priority,l.revision,l.enabled,l.paused,l.pause_reason,l.scope_kind,l.scope_node_id::text,l.policy,l.created_at,l.updated_at`
const source = ` FROM autopilot_lanes l JOIN nodes n ON n.tenant_id=l.tenant_id AND n.id=l.node_id AND n.project_id=l.project_id AND n.parent_id=l.project_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug='autopilot_lane'`

func scan(row pgx.Row) (Lane, error) {
	var l Lane
	var raw []byte
	err := row.Scan(&l.ID, &l.ProjectID, &l.OwnerPrincipalID, &l.Name, &l.Priority, &l.Revision, &l.Enabled, &l.Paused, &l.PauseReason, &l.Scope.Kind, &l.Scope.ReleaseNodeID, &raw, &l.CreatedAt, &l.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &l.Policy)
	}
	// Fail closed if a projection writer stored malformed policy; preview must
	// never interpret unchecked work-window endpoints or resource ceilings.
	if err == nil {
		err = validatePolicy(l.Policy)
	}
	if err == nil {
		err = validateScope(l.Scope)
	}
	return l, err
}
func load(ctx context.Context, tx pgx.Tx, id string, write bool) (Lane, error) {
	query := `SELECT ` + columns + source + ` AND l.node_id=$1`
	if write {
		query += ` FOR NO KEY UPDATE OF n,l`
	}
	return scan(tx.QueryRow(ctx, query, id))
}
func executionAuthority(ctx context.Context, tx pgx.Tx, actor tenant.Principal, l Lane) error {
	if l.Scope.Kind != "queued_tickets" {
		return fail(409, "release execution awaits the ordered release integration")
	}
	for _, id := range []string{actor.ID, l.OwnerPrincipalID} {
		owner := tenant.Principal{ID: id, TenantID: actor.TenantID, Kind: tenant.Person}
		for _, key := range []string{"nodes.read", "run.create", "work_orders.write", "work_orders.assign", "runs.write"} {
			if err := permission(ctx, tx, owner, key, l.ProjectID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var out Lane
	err := m.transaction(r, p, false, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = load(ctx, tx, r.PathValue("laneId"), false)
		if err != nil {
			return err
		}
		return permission(ctx, tx, p, "autopilot.read", out.ProjectID)
	})
	respond(w, 200, out, err)
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	limit, err := pageLimit(r)
	if err != nil {
		respond(w, 200, nil, err)
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" && !uuid.MatchString(after) {
		respond(w, 200, nil, fail(400, "invalid after"))
		return
	}
	out := struct {
		Items []Lane  `json:"items"`
		Next  *string `json:"next_after"`
	}{Items: []Lane{}}
	err = m.transaction(r, p, false, func(ctx context.Context, tx pgx.Tx) error {
		id := r.PathValue("projectId")
		if err := project(ctx, tx, id); err != nil {
			return err
		}
		if err := permission(ctx, tx, p, "autopilot.read", id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+columns+source+` AND l.project_id=$1 AND ($2::uuid IS NULL OR l.node_id>$2) ORDER BY l.node_id LIMIT $3`, id, nullID(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scan(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, l)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.Next = &out.Items[limit-1].ID
		}
		return rows.Err()
	})
	respond(w, 200, out, err)
}
func nullID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

type createInput struct {
	Name     string `json:"name"`
	Priority *int   `json:"priority"`
	Scope    *Scope `json:"scope"`
	Policy   Policy `json:"policy"`
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in createInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 201, nil, err)
		return
	}
	l := Lane{ProjectID: r.PathValue("projectId"), OwnerPrincipalID: p.ID, Name: strings.TrimSpace(in.Name), Priority: 100, Scope: Scope{Kind: "queued_tickets"}, Policy: in.Policy}
	if in.Priority != nil {
		l.Priority = *in.Priority
	}
	if in.Scope != nil {
		l.Scope = *in.Scope
	}
	if err := validateLane(l); err != nil {
		respond(w, 201, nil, err)
		return
	}
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		if err := project(ctx, tx, l.ProjectID); err != nil {
			return err
		}
		if err := permission(ctx, tx, p, "autopilot.manage", l.ProjectID); err != nil {
			return err
		}
		if err := scopeNode(ctx, tx, l.Scope, l.ProjectID); err != nil {
			return err
		}
		// Lazy tenant-kind installation supports both existing and new tenants.
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'autopilot_lane','Autopilot lane','LANE','agents',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO NOTHING`, p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,$3,'disabled' FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='autopilot_lane' RETURNING id::text`, p.TenantID, l.Name, l.ProjectID).Scan(&l.ID); err != nil {
			return err
		}
		raw, err := json.Marshal(l.Policy)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `INSERT INTO autopilot_lanes(tenant_id,node_id,project_id,owner_principal_id,name,priority,scope_kind,scope_node_id,policy) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING revision,created_at,updated_at`, p.TenantID, l.ID, l.ProjectID, p.ID, l.Name, l.Priority, l.Scope.Kind, l.Scope.ReleaseNodeID, raw).Scan(&l.Revision, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &l.ID, Type: "node.autopilot_lane_created", After: l})
		return err
	})
	respond(w, 201, l, err)
}
func validateLane(l Lane) error {
	if !boundedText(l.Name, 120, false) || l.Priority < 0 || l.Priority > 1000 {
		return fail(400, "name at most 120 bytes and priority 0..1000 required")
	}
	if err := validateScope(l.Scope); err != nil {
		return fail(400, err.Error())
	}
	if err := validatePolicy(l.Policy); err != nil {
		return fail(400, err.Error())
	}
	return nil
}

type patchInput struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Name             *string `json:"name"`
	Priority         *int    `json:"priority"`
	Scope            *Scope  `json:"scope"`
	Policy           *Policy `json:"policy"`
	Enabled          *bool   `json:"enabled"`
}

func (m *Module) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in patchInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 200, nil, err)
		return
	}
	if in.ExpectedRevision < 1 || in.Name == nil && in.Priority == nil && in.Scope == nil && in.Policy == nil && in.Enabled == nil {
		respond(w, 200, nil, fail(400, "revision and at least one policy change required"))
		return
	}
	var out Lane
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		before, err := load(ctx, tx, r.PathValue("laneId"), true)
		if err != nil {
			return err
		}
		if err = permission(ctx, tx, p, "autopilot.manage", before.ProjectID); err != nil {
			return err
		}
		if before.Revision != in.ExpectedRevision {
			return fail(409, "lane changed; reload")
		}
		out = before
		if in.Name != nil {
			out.Name = strings.TrimSpace(*in.Name)
		}
		if in.Priority != nil {
			out.Priority = *in.Priority
		}
		if in.Scope != nil {
			out.Scope = *in.Scope
		}
		if in.Policy != nil {
			out.Policy = *in.Policy
		}
		if in.Enabled != nil {
			out.Enabled = *in.Enabled
		}
		if err = validateLane(out); err != nil {
			return err
		}
		if err = scopeNode(ctx, tx, out.Scope, out.ProjectID); err != nil {
			return err
		}
		if out.Enabled {
			if err = executionAuthority(ctx, tx, p, out); err != nil {
				return err
			}
		}
		if reflect.DeepEqual(out, before) {
			return nil
		}
		return save(ctx, tx, p, &out, before, "node.autopilot_lane_updated")
	})
	respond(w, 200, out, err)
}

type actionInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

func (m *Module) pause(w http.ResponseWriter, r *http.Request)  { m.action(w, r, false) }
func (m *Module) resume(w http.ResponseWriter, r *http.Request) { m.action(w, r, true) }
func (m *Module) action(w http.ResponseWriter, r *http.Request, resume bool) {
	p, ok := principal(w, r, resume)
	if !ok {
		return
	}
	var in actionInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 200, nil, err)
		return
	}
	if in.ExpectedRevision < 1 || !boundedText(in.Reason, 500, true) {
		respond(w, 200, nil, fail(400, "revision and reason of at most 500 bytes required"))
		return
	}
	var out Lane
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		before, err := load(ctx, tx, r.PathValue("laneId"), true)
		if err != nil {
			return err
		}
		key := "autopilot.pause"
		if resume {
			key = "autopilot.manage"
		}
		if err = permission(ctx, tx, p, key, before.ProjectID); err != nil {
			return err
		}
		if p.Kind == tenant.Agent {
			if err = authz.RequireQueueCoordinatorTx(ctx, tx, p, before.ProjectID); err != nil {
				return err
			}
		}
		if before.Revision != in.ExpectedRevision {
			return fail(409, "lane changed; reload")
		}
		out = before
		event := "node.autopilot_lane_paused"
		if resume {
			if err = executionAuthority(ctx, tx, p, out); err != nil {
				return err
			}
			out.Enabled = true
			out.Paused = false
			out.PauseReason = ""
			event = "node.autopilot_lane_resumed"
		} else {
			out.Paused = true
			out.PauseReason = in.Reason
		}
		if reflect.DeepEqual(out, before) {
			return nil
		}
		return save(ctx, tx, p, &out, before, event)
	})
	respond(w, 200, out, err)
}

// All record mutations happen before Append. Never acquire a lock afterwards.
func save(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *Lane, before Lane, event string) error {
	raw, err := json.Marshal(l.Policy)
	if err != nil {
		return err
	}
	state := "disabled"
	if l.Enabled {
		state = "enabled"
	}
	if l.Paused {
		state = "paused"
	}
	if _, err = tx.Exec(ctx, `UPDATE nodes SET title=$2,state=$3,updated_at=clock_timestamp() WHERE id=$1`, l.ID, l.Name, state); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `UPDATE autopilot_lanes SET name=$2,priority=$3,enabled=$4,paused=$5,pause_reason=$6,scope_kind=$7,scope_node_id=$8,policy=$9,revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1 RETURNING revision,updated_at`, l.ID, l.Name, l.Priority, l.Enabled, l.Paused, l.PauseReason, l.Scope.Kind, l.Scope.ReleaseNodeID, raw).Scan(&l.Revision, &l.UpdatedAt); err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &l.ID, Type: event, Before: before, After: l})
	return err
}
