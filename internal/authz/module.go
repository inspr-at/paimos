// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/identity"
	"github.com/inspr-at/paimos/internal/tenant"
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Module struct {
	pool        *pgxpool.Pool
	provisioner identity.Provisioner
}

func New(pool *pgxpool.Pool) httpapi.Module { return &Module{pool: pool} }

// NewWithProvisioner lets the coordinator inject the config-selected identity
// adapter without giving authz access to server environment or credentials.
// The adapter must also implement identity.TenantScoped; otherwise it is not
// advertised and provisioning fails closed.
func NewWithProvisioner(pool *pgxpool.Pool, provisioner identity.Provisioner) httpapi.Module {
	return &Module{pool: pool, provisioner: provisioner}
}
func (m *Module) provisionerFor(tenantID string) identity.Provisioner {
	if m.provisioner == nil {
		return nil
	}
	scoped, ok := m.provisioner.(identity.TenantScoped)
	if !ok || scoped.TenantID() != tenantID {
		return nil
	}
	return m.provisioner
}
func (m *Module) Mount(mux *http.ServeMux) {
	Handle(mux, m.pool, "GET /api/authz/permissions", "roles.read", m.permissions)
	Handle(mux, m.pool, "GET /api/me/permissions", "authz.read", m.mePermissions)
	Handle(mux, m.pool, "GET /api/roles", "roles.read", m.roles)
	Handle(mux, m.pool, "POST /api/roles", "roles.manage", m.createRole)
	Handle(mux, m.pool, "PATCH /api/roles/{id}", "roles.manage", m.patchRole)
	Handle(mux, m.pool, "DELETE /api/roles/{id}", "roles.manage", m.deleteRole)
	Handle(mux, m.pool, "GET /api/members", "members.read", m.members)
	Handle(mux, m.pool, "PUT /api/members/{principal_id}/workspace-role", "members.manage", m.putWorkspaceRole)
	Handle(mux, m.pool, "POST /api/members/invites", "members.manage", m.createInvite)
	Handle(mux, m.pool, "POST /api/members/invites/{id}/provision", "members.manage", m.retryInviteProvision)
	Handle(mux, m.pool, "DELETE /api/members/invites/{id}", "members.manage", m.revokeInvite)
	Handle(mux, m.pool, "POST /api/members/{principal_id}/deactivate", "members.manage", m.deactivate)
	Handle(mux, m.pool, "POST /api/members/{principal_id}/reactivate", "members.manage", m.reactivate)
	Handle(mux, m.pool, "POST /api/members/{principal_id}/aliases", "members.manage", m.linkAlias)
	Handle(mux, m.pool, "DELETE /api/members/{principal_id}/aliases/{from_principal_id}", "members.manage", m.unlinkAlias)
	Handle(mux, m.pool, "GET /api/audit", "audit.read", m.audit)
	Handle(mux, m.pool, "GET /api/projects/{projectId}/members", "members.read", m.projectMembers)
	Handle(mux, m.pool, "PUT /api/projects/{projectId}/members/{principal_id}", "members.manage", m.putProjectMember)
	Handle(mux, m.pool, "DELETE /api/projects/{projectId}/members/{principal_id}", "members.manage", m.deleteProjectMember)
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}
func apiFail(w http.ResponseWriter, status int, code, field, reason string) {
	out := map[string]any{"error": reason, "code": code, "reason": reason}
	if field != "" {
		out["field"] = field
	}
	reply(w, status, out)
}
func internalFail(w http.ResponseWriter, err error) {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		apiFail(w, 404, "not_found", "", "Resource not found")
	case errors.Is(err, ErrForbidden):
		apiFail(w, 403, "forbidden", "", "Permission denied")
	case lastOwnerViolation(err):
		apiFail(w, 409, "last_owner", "role_id", "The last active owner cannot be removed")
	case errors.As(err, &pe) && strings.HasPrefix(pe.Code, "23"):
		apiFail(w, 409, "conflict", "", "The change conflicts with an existing record")
	default:
		slog.Error("authz", "err", err)
		apiFail(w, 500, "internal", "", "Internal error")
	}
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}
func actor(r *http.Request) tenant.Principal                         { p, _ := tenant.PrincipalFrom(r.Context()); return p }
func (m *Module) permissions(w http.ResponseWriter, _ *http.Request) { reply(w, 200, Registry) }
func (m *Module) mePermissions(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project_id")
	if project != "" && !uuidPattern.MatchString(project) {
		apiFail(w, 400, "invalid", "project_id", "Project ID must be a UUID")
		return
	}
	effective, err := Load(r.Context(), m.pool, actor(r), project)
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 200, effective)
}

type Role struct {
	ID          string   `json:"id"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
	BasedOn     *string  `json:"based_on"`
	MemberCount int      `json:"member_count"`
}

func roleTx(ctx context.Context, tx pgx.Tx, id string) (Role, error) {
	var out Role
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.key,r.name,r.description,r.builtin,r.based_on::text,
	  (SELECT count(DISTINCT b.principal_id) FROM role_bindings b WHERE b.tenant_id=r.tenant_id AND b.role_id=r.id)
      FROM roles r WHERE r.id=$1::uuid`, id).Scan(&out.ID, &out.Key, &out.Name, &out.Description, &out.Builtin, &out.BasedOn, &out.MemberCount)
	if err != nil {
		return Role{}, err
	}
	if out.Builtin {
		out.Permissions = builtinPermissions(out.Key)
		return out, nil
	}
	out.Permissions = []string{}
	rows, err := tx.Query(ctx, `SELECT permission FROM role_permissions WHERE role_id=$1::uuid ORDER BY permission`, id)
	if err != nil {
		return Role{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return Role{}, err
		}
		out.Permissions = append(out.Permissions, key)
	}
	return out, rows.Err()
}
func (m *Module) roles(w http.ResponseWriter, r *http.Request) {
	out := []Role{}
	err := db.InTenant(r.Context(), m.pool, actor(r).TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id::text FROM roles ORDER BY builtin DESC,name,id`)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			role, err := roleTx(r.Context(), tx, id)
			if err != nil {
				return err
			}
			out = append(out, role)
		}
		return nil
	})
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 200, out)
}

type roleWrite struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Permissions *[]string `json:"permissions"`
	BasedOn     *string   `json:"based_on"`
}

func validateRoleWrite(body roleWrite, create bool) (string, string, []string, error) {
	if create && (body.Name == nil || body.Permissions == nil) {
		return "", "", nil, errors.New("name and permissions are required")
	}
	name := ""
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
		if name == "" || len(name) > 100 {
			return "", "", nil, errors.New("invalid name")
		}
	}
	desc := ""
	if body.Description != nil {
		desc = strings.TrimSpace(*body.Description)
		if len(desc) > 500 {
			return "", "", nil, errors.New("description is too long")
		}
	}
	perms := []string{}
	if body.Permissions != nil {
		if len(*body.Permissions) > len(Registry) {
			return "", "", nil, errors.New("too many permissions")
		}
		seen := map[string]bool{}
		for _, key := range *body.Permissions {
			if _, ok := Lookup(key); !ok || seen[key] {
				return "", "", nil, fmt.Errorf("invalid permission %q", key)
			}
			seen[key] = true
			perms = append(perms, key)
		}
	}
	return name, desc, perms, nil
}
func canGrantTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, perms []string) error {
	own, err := loadTx(ctx, tx, p, "")
	if err != nil {
		return err
	}
	for _, key := range perms {
		if !contains(own.Workspace.Permissions, key) {
			return ErrForbidden
		}
	}
	return nil
}

// Serialize access-management changes per tenant, then recheck the actor's
// current binding. This closes the check/write race with concurrent demotion.
func (m *Module) authorizeMutation(ctx context.Context, tx pgx.Tx, p tenant.Principal, required string, grants []string) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, p.TenantID).Scan(&id); err != nil {
		return err
	}
	if err := requireTx(ctx, tx, p, required, Scope{}); err != nil {
		return err
	}
	return canGrantTx(ctx, tx, p, grants)
}
func (m *Module) createRole(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	var body roleWrite
	if err := decode(r, &body); err != nil {
		apiFail(w, 400, "invalid", "body", err.Error())
		return
	}
	name, description, perms, err := validateRoleWrite(body, true)
	if err != nil {
		apiFail(w, 400, "invalid", "permissions", err.Error())
		return
	}
	if body.BasedOn != nil && !uuidPattern.MatchString(*body.BasedOn) {
		apiFail(w, 400, "invalid", "based_on", "Role ID must be a UUID")
		return
	}
	var out Role
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "roles.manage", perms); err != nil {
			return err
		}
		if body.BasedOn != nil {
			if _, err := roleTx(r.Context(), tx, *body.BasedOn); err != nil {
				return err
			}
		}
		var id string
		if err := tx.QueryRow(r.Context(), `WITH next AS (SELECT gen_random_uuid() AS id)
          INSERT INTO roles(tenant_id,id,key,name,description,based_on)
          SELECT $1::uuid,next.id,'custom_'||replace(next.id::text,'-',''),$2,$3,$4::uuid FROM next
          RETURNING id::text`, p.TenantID, name, description, body.BasedOn).Scan(&id); err != nil {
			return err
		}
		for _, key := range perms {
			if _, err := tx.Exec(r.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3)`, p.TenantID, id, key); err != nil {
				return err
			}
		}
		out, err = roleTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		err = appendEvent(r.Context(), tx, p, "authz.role_created", nil, out)
		return err
	})
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 201, out)
}
func (m *Module) patchRole(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		apiFail(w, 400, "invalid", "id", "Role ID must be a UUID")
		return
	}
	var body roleWrite
	if err := decode(r, &body); err != nil {
		apiFail(w, 400, "invalid", "body", err.Error())
		return
	}
	name, description, perms, err := validateRoleWrite(body, false)
	if err != nil {
		apiFail(w, 400, "invalid", "permissions", err.Error())
		return
	}
	if body.BasedOn != nil {
		apiFail(w, 400, "invalid", "based_on", "Based-on role cannot change")
		return
	}
	var out Role
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "roles.manage", perms); err != nil {
			return err
		}
		before, err := roleTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if before.Builtin {
			return ErrForbidden
		}
		if body.Name != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE roles SET name=$1 WHERE id=$2::uuid`, name, id); err != nil {
				return err
			}
		}
		if body.Description != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE roles SET description=$1 WHERE id=$2::uuid`, description, id); err != nil {
				return err
			}
		}
		if body.Permissions != nil {
			if _, err := tx.Exec(r.Context(), `DELETE FROM role_permissions WHERE role_id=$1::uuid`, id); err != nil {
				return err
			}
			for _, key := range perms {
				if _, err := tx.Exec(r.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3)`, p.TenantID, id, key); err != nil {
					return err
				}
			}
		}
		out, err = roleTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		err = appendEvent(r.Context(), tx, p, "authz.role_updated", before, out)
		return err
	})
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 200, out)
}
func (m *Module) deleteRole(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("id")
	target := r.URL.Query().Get("reassign_to")
	if !uuidPattern.MatchString(id) || target != "" && !uuidPattern.MatchString(target) {
		apiFail(w, 400, "invalid", "id", "Role IDs must be UUIDs")
		return
	}
	if id == target {
		apiFail(w, 400, "invalid", "reassign_to", "Choose another role")
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "roles.manage", nil); err != nil {
			return err
		}
		before, err := roleTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if before.Builtin {
			return ErrForbidden
		}
		if before.MemberCount > 0 && target == "" {
			return errRoleInUse
		}
		if target != "" {
			replacement, err := roleTx(r.Context(), tx, target)
			if err != nil {
				return err
			}
			if err := canGrantTx(r.Context(), tx, p, replacement.Permissions); err != nil {
				return err
			}
			if replacement.Key == "owner" {
				if err := requireTx(r.Context(), tx, p, "ownership.transfer", Scope{}); err != nil {
					return err
				}
			}
			rows, err := tx.Query(r.Context(), `SELECT id::text,principal_id::text,scope_type,scope_id::text FROM role_bindings WHERE role_id=$1::uuid`, id)
			if err != nil {
				return err
			}
			type binding struct {
				id, principal, scope string
				project              *string
			}
			items := []binding{}
			for rows.Next() {
				var b binding
				if err := rows.Scan(&b.id, &b.principal, &b.scope, &b.project); err != nil {
					rows.Close()
					return err
				}
				items = append(items, b)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, b := range items {
				if _, err := tx.Exec(r.Context(), `UPDATE role_bindings SET role_id=$1::uuid WHERE id=$2::uuid`, target, b.id); err != nil {
					return err
				}
				if err := appendEvent(r.Context(), tx, p, "authz.binding_reassigned", map[string]any{"principal_id": b.principal, "role_id": id, "scope_type": b.scope, "scope_id": b.project}, map[string]any{"principal_id": b.principal, "role_id": target, "scope_type": b.scope, "scope_id": b.project}); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM roles WHERE id=$1::uuid`, id); err != nil {
			return err
		}
		err = appendEvent(r.Context(), tx, p, "authz.role_deleted", before, nil)
		return err
	})
	if errors.Is(err, errRoleInUse) {
		apiFail(w, 409, "role_in_use", "reassign_to", "Role has members; choose a replacement")
		return
	}
	if err != nil {
		internalFail(w, err)
		return
	}
	w.WriteHeader(204)
}

var errRoleInUse = errors.New("role in use")

func lastOwnerViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23514" && strings.Contains(pe.Message, "last active owner")
}

// grantError is a role whose permissions the actor does not hold.
type grantError struct{ field string }

func (e grantError) Error() string { return "grant exceeds your permissions" }
