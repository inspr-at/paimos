// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) httpapi.Module { return &Module{pool: pool} }

type endpoint func(*http.Request, pgx.Tx, tenant.Principal) (any, error)

func (m *Module) Mount(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern, permission string
		handler             endpoint
	}{
		{"GET /api/rules/layers", "rules.read", m.layers}, {"POST /api/rules/layers", "rules.write", m.createLayer},
		{"GET /api/rules/sets", "rules.read", m.sets}, {"POST /api/rules/sets", "rules.write", m.createSet},
		{"GET /api/rules/sets/{setId}", "rules.read", m.getSet}, {"PUT /api/rules/sets/{setId}/draft", "rules.write", m.draft},
		{"POST /api/rules/sets/{setId}/publish", "rules.publish", m.publish}, {"POST /api/rules/sets/{setId}/restore", "rules.publish", m.restore},
		{"GET /api/rules/sets/{setId}/versions", "rules.read", m.versions}, {"GET /api/rules/sets/{setId}/versions/{version}", "rules.read", m.version},
		{"GET /api/rules/merged", "rules.read", m.merged},
		{"POST /api/rules/publish", "rules.publish", m.publishBatch},
	} {
		mux.HandleFunc(route.pattern, m.endpoint(route.permission, route.handler))
	}
}
func (m *Module) endpoint(permission string, fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
			writeFailure(w, fail(401, "unauthorized", "authentication required"))
			return
		}
		if p.Kind != tenant.Person && p.Kind != tenant.Agent {
			writeFailure(w, authz.ErrForbidden)
			return
		}
		if id := r.PathValue("setId"); id != "" && !workorders.UUID(id) {
			writeFailure(w, fail(400, "invalid_scope", "invalid set UUID"))
			return
		}
		if permission == "rules.publish" && p.Kind != tenant.Person {
			writeFailure(w, authz.ErrForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var out any
		err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
			if err := authz.RequireTx(r.Context(), tx, p, permission, authz.Scope{AnyProject: true}); err != nil {
				return err
			}
			owner, err := actorOwner(r.Context(), tx, p)
			if err != nil {
				return err
			}
			agent := ""
			if p.Kind == tenant.Agent {
				agent = p.ID
			}
			// Preserve the original project visibility for rule RLS before enabling
			// workspace company/person nodes. No generic API runs in this transaction.
			var visibleProjects string
			if err = tx.QueryRow(r.Context(), `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&visibleProjects); err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `SELECT set_config('aeon.rules_projects',$3,true),set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_agent',$2,true),set_config('aeon.rules_access','on',true),set_config('aeon.visible_projects','*',true)`, owner, agent, visibleProjects)
			if err != nil {
				return err
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0)),set_config('aeon.rules_write','on',true)`); err != nil {
					return err
				}
				if err = ensureKinds(r.Context(), tx, p); err != nil {
					return err
				}
			}
			out, err = fn(r, tx, p)
			return err
		})
		if err != nil {
			writeFailure(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, out)
	}
}
func writeFailure(w http.ResponseWriter, err error) {
	var e *Error
	var we *workorders.Error
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &e):
	case errors.Is(err, authz.ErrForbidden):
		e = &Error{Status: 403, Code: "forbidden", Message: "permission or scoped ownership denied"}
	case errors.Is(err, pgx.ErrNoRows):
		e = &Error{Status: 404, Code: "not_found", Message: "rule resource unavailable"}
	case errors.As(err, &we):
		e = &Error{Status: we.Status, Code: "invalid_request", Message: we.Message}
	case errors.As(err, &pe) && pe.Code == "23505":
		e = &Error{Status: 409, Code: "revision_conflict", Message: "rule identity or version already exists"}
	default:
		e = &Error{Status: 500, Code: "internal_error", Message: "rule operation failed"}
	}
	httpapi.WriteJSON(w, e.Status, e)
}
func actorOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	id := p.ID
	if p.Kind == tenant.Agent {
		id = p.KeyCreatorID
	}
	if !workorders.UUID(id) {
		return "", authz.ErrForbidden
	}
	var owner string
	err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active'`, p.TenantID, id).Scan(&owner)
	return owner, err
}
func permission(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Scope, action string) error {
	if err := ValidateScope(s); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, action, authz.Scope{ProjectID: s.ProjectID, AnyProject: action == "rules.read" && s.ProjectID == ""}); err != nil {
		return err
	}
	if action == "rules.publish" && p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return err
	}
	if s.OwnerID != "" && s.OwnerID != owner {
		return authz.ErrForbidden
	}
	if s.OwnerID != "" {
		if err = principalExists(ctx, tx, s.OwnerID, "person"); err != nil {
			return err
		}
	}
	if s.AgentID != "" {
		if p.Kind == tenant.Agent {
			if s.AgentID != p.ID {
				return authz.ErrForbidden
			}
		} else {
			var controlled bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_keys k JOIN principals a ON a.tenant_id=k.tenant_id AND a.id=k.principal_id WHERE k.tenant_id=$1 AND k.principal_id=$2 AND k.created_by_principal_id=$3 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND a.kind='agent' AND a.status='active')`, p.TenantID, s.AgentID, owner).Scan(&controlled)
			if err != nil {
				return err
			}
			if !controlled {
				return authz.ErrForbidden
			}
		}
		if err = principalExists(ctx, tx, s.AgentID, "agent"); err != nil {
			return err
		}
	}
	if s.ProjectID != "" {
		if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: s.ProjectID}); err != nil {
			return err
		}
		var found bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL)`, s.ProjectID).Scan(&found)
		if err != nil {
			return err
		}
		if !found {
			return pgx.ErrNoRows
		}
	}
	if s.TaskID != "" {
		var found bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL)`, s.TaskID, s.ProjectID).Scan(&found)
		if err != nil {
			return err
		}
		if !found {
			return pgx.ErrNoRows
		}
	}
	if action != "rules.read" {
		if s.Layer == "company" {
			if p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			return authz.RequireTx(ctx, tx, p, "rules.publish", authz.Scope{})
		}
		// Workspace role sets and project work-product rules belong to their
		// administrators. Agents may draft only within their creator's authority.
		if s.Layer == "project" || s.Role != "" {
			person := tenant.Principal{ID: owner, TenantID: p.TenantID, Kind: tenant.Person}
			return authz.RequireTx(ctx, tx, person, "rules.publish", authz.Scope{ProjectID: s.ProjectID})
		}
	}
	return nil
}
func principalExists(ctx context.Context, tx pgx.Tx, id, kind string) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind=$2 AND status='active')`, id, kind).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return pgx.ErrNoRows
	}
	return nil
}
func (m *Module) merged(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	q := r.URL.Query()
	allowed := map[string]bool{"project_id": true, "person_id": true, "agent_id": true, "role": true, "harness": true, "task_id": true}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return nil, fail(400, "invalid_scope", "unknown or repeated merge selector")
		}
	}
	c := Context{p.TenantID, q.Get("project_id"), q.Get("person_id"), q.Get("agent_id"), q.Get("role"), q.Get("harness"), q.Get("task_id")}
	if err := ValidateContext(c); err != nil {
		return nil, err
	}
	owner, err := actorOwner(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	if owner != c.PersonID {
		return nil, authz.ErrForbidden
	}
	if p.Kind == tenant.Agent && c.AgentID != p.ID {
		return nil, authz.ErrForbidden
	}
	scope := Scope{Layer: "project", ProjectID: c.ProjectID}
	if err = permission(r.Context(), tx, p, scope, "rules.read"); err != nil {
		return nil, err
	}
	if c.AgentID != "" {
		scope = Scope{Layer: "agent", OwnerID: c.PersonID, AgentID: c.AgentID}
		if c.TaskID != "" {
			scope.TaskID = c.TaskID
			scope.ProjectID = c.ProjectID
		}
		if err = permission(r.Context(), tx, p, scope, "rules.read"); err != nil {
			return nil, err
		}
	}
	ss, err := allSets(r.Context(), tx, "")
	if err != nil {
		return nil, err
	}
	snapshots := []Snapshot{}
	for _, s := range ss {
		if !s.Scope.matches(c) || s.PublishedVersion == "" {
			continue
		}
		if err = permission(r.Context(), tx, p, s.Scope, "rules.read"); err != nil {
			return nil, err
		}
		snap, err := loadVersion(r.Context(), tx, s.ID, s.PublishedVersion)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snap)
	}
	return Merge(c, snapshots, time.Now().UTC())
}

// fields is the storage envelope on ordinary Aeon nodes. Scope is copied onto
// every child so RLS never needs a recursive parent lookup.
type fields struct {
	Resource         string    `json:"_aeon_rule_resource"`
	Scope            Scope     `json:"scope"`
	Name             string    `json:"name,omitempty"`
	Revision         int64     `json:"revision,omitempty"`
	PublishedVersion string    `json:"published_version,omitempty"`
	Rule             *Rule     `json:"rule,omitempty"`
	Version          string    `json:"version,omitempty"`
	Snapshot         *Snapshot `json:"snapshot,omitempty"`
}

func decodeFields(raw []byte) (fields, error) {
	var f fields
	err := json.Unmarshal(raw, &f)
	return f, err
}
func ensureKinds(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	_, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES ($1,'aeon_rule_layer','Rule layer','ARL','shield'),($1,'aeon_rule_set','Rule set','ARS','shield'),($1,'aeon_rule','Rule','ARR','shield'),($1,'aeon_rule_version','Rule version','ARV','shield') ON CONFLICT (tenant_id,slug) DO NOTHING`, p.TenantID)
	return err
}
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func resourceSlug(resource string) string {
	if resource == "rule" {
		return "aeon_rule"
	}
	return "aeon_rule_" + resource
}
func insertNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, parent, title string, f fields) (string, error) {
	var id string
	var parentID any
	if parent != "" {
		parentID = parent
	}
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields) SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,$2,$3,$4 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug=$5 RETURNING id::text`, p.TenantID, title, parentID, jsonBytes(f), resourceSlug(f.Resource)).Scan(&id)
	return id, err
}
func nodeFields(ctx context.Context, tx pgx.Tx, id, resource string) (fields, string, error) {
	if !workorders.UUID(id) {
		return fields{}, "", fail(400, "invalid_scope", "invalid resource UUID")
	}
	var raw []byte
	var parent string
	err := tx.QueryRow(ctx, `SELECT fields,coalesce(parent_id::text,'') FROM nodes WHERE id=$1 AND rule_resource=$2 AND deleted_at IS NULL`, id, resource).Scan(&raw, &parent)
	if err != nil {
		return fields{}, "", err
	}
	f, err := decodeFields(raw)
	return f, parent, err
}
func isDenied(err error) bool {
	return errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows)
}
