// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ProjectMember is one principal with access to a project (contract v2
// decision 4): role is the project binding's role when there is one, else the
// workspace role that opens every project.
type ProjectMember struct {
	PrincipalID   string   `json:"principal_id"`
	Name          string   `json:"name"`
	AvatarURL     *string  `json:"avatar_url"`
	HasAvatar     bool     `json:"has_avatar"`
	Kind          string   `json:"kind"`
	Via           string   `json:"via"`
	Role          RoleRef  `json:"role"`
	WorkspaceRole *RoleRef `json:"workspace_role"`
}

// ProjectBinding is the result of granting a project role.
type ProjectBinding struct {
	ID          string    `json:"id"`
	PrincipalID string    `json:"principal_id"`
	ProjectID   string    `json:"project_id"`
	ScopeType   string    `json:"scope_type"`
	Role        RoleRef   `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

var (
	errProjectRole   = errors.New("role cannot be granted on a project")
	errViaWorkspace  = errors.New("access comes from the workspace role")
	errNotBindable   = errors.New("principal cannot be bound")
	errNoSuchBinding = errors.New("no project binding")
)

// projectRoleAllowed: Owner is a workspace role and Customer the quote portal;
// every other built-in and every custom role may be granted on a project,
// where it grants only its project-grantable permissions.
func projectRoleAllowed(role Role) bool {
	return !role.Builtin || role.Key != "owner" && role.Key != "customer"
}

func projectNodeTx(ctx context.Context, tx pgx.Tx, projectID string) (key, title string, err error) {
	err = tx.QueryRow(ctx, `SELECT n.key,n.title FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug='project'`, projectID).Scan(&key, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errNotProject
	}
	return key, title, err
}

func (m *Module) projectMembers(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	projectID := r.PathValue("projectId")
	if !uuidPattern.MatchString(projectID) {
		apiFail(w, 400, "invalid", "id", "Project ID must be a UUID")
		return
	}
	out := []ProjectMember{}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, _, err := projectNodeTx(r.Context(), tx, projectID); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
          SELECT p.id::text,p.name,p.kind,
                 EXISTS (SELECT 1 FROM personal_profiles pp WHERE pp.tenant_id=p.tenant_id AND pp.principal_id=p.id AND pp.avatar_hashes <> '{}'::jsonb),
                 wr.id::text,wr.key,wr.name,
                 pr.id::text,pr.key,pr.name,
                 wb.role_id IS NOT NULL AND aeon_role_reads_nodes(p.tenant_id,wb.role_id,'workspace')
          FROM principals p
          LEFT JOIN role_bindings wb ON wb.tenant_id=p.tenant_id AND wb.principal_id=p.id AND wb.scope_type='workspace'
          LEFT JOIN roles wr ON wr.tenant_id=wb.tenant_id AND wr.id=wb.role_id
          LEFT JOIN role_bindings pb ON pb.tenant_id=p.tenant_id AND pb.principal_id=p.id AND pb.scope_type='project' AND pb.scope_id=$1::uuid
          LEFT JOIN roles pr ON pr.tenant_id=pb.tenant_id AND pr.id=pb.role_id
          WHERE p.status='active' AND p.linked_to IS NULL
            AND NOT (p.kind='agent' AND p.roles && ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service']::text[])
            AND (pb.id IS NOT NULL OR wb.role_id IS NOT NULL AND aeon_role_reads_nodes(p.tenant_id,wb.role_id,'workspace'))
          ORDER BY p.kind DESC,lower(p.name),p.id`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ProjectMember
			var wID, wKey, wName, pID, pKey, pName *string
			var opens bool
			if err := rows.Scan(&item.PrincipalID, &item.Name, &item.Kind, &item.HasAvatar, &wID, &wKey, &wName, &pID, &pKey, &pName, &opens); err != nil {
				return err
			}
			if wID != nil {
				item.WorkspaceRole = &RoleRef{ID: *wID, Key: *wKey, Name: *wName}
			}
			if pID != nil {
				item.Via, item.Role = "project", RoleRef{ID: *pID, Key: *pKey, Name: *pName}
			} else {
				item.Via, item.Role = "workspace", *item.WorkspaceRole
			}
			if item.HasAvatar {
				url := "/api/people/" + item.PrincipalID + "/avatar"
				item.AvatarURL = &url
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	if err != nil {
		projectFail(w, err)
		return
	}
	reply(w, 200, out)
}

func projectFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotProject):
		apiFail(w, 404, "not_found", "id", "Project not found")
	case errors.Is(err, errProjectRole):
		apiFail(w, 400, "invalid", "role_id", "Owner and Customer are workspace roles; choose a project role")
	case errors.Is(err, errViaWorkspace):
		apiFail(w, 409, "via_workspace", "principal_id", "This access comes from the workspace role; change it in Members")
	case errors.Is(err, errNotBindable):
		apiFail(w, 400, "invalid", "principal_id", "Only active people and agents can be given project access")
	case errors.Is(err, errNoSuchBinding):
		apiFail(w, 404, "not_found", "principal_id", "No project access to remove")
	default:
		internalFail(w, err)
	}
}

// authorizeProjectMutation serializes access changes per tenant, then checks
// members.manage on the project and that the actor holds every permission the
// role would grant there.
func (m *Module) authorizeProjectMutation(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, grants []string) error {
	if err := lockProjectMutation(ctx, tx, p.TenantID); err != nil {
		return err
	}
	if err := requireTx(ctx, tx, p, "members.manage", Scope{ProjectID: projectID}); err != nil {
		return err
	}
	own, err := loadTx(ctx, tx, p, projectID)
	if err != nil {
		return err
	}
	for _, key := range grants {
		if canGrantExplicitWatch(p, own, key) {
			continue
		}
		if !contains(own.Project.Permissions, key) {
			return ErrForbidden
		}
	}
	return nil
}

// lockProjectMutation serializes project bindings with other membership
// changes in the tenant. The advisory lock also covers operator CLI calls.
func lockProjectMutation(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, tenantID); err != nil {
		return err
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, tenantID).Scan(&id); err != nil {
		return err
	}
	return nil
}

func bindableTx(ctx context.Context, tx pgx.Tx, tenantID, principalID string) error {
	var kind, status string
	var linked bool
	var legacy []string
	err := tx.QueryRow(ctx, `SELECT kind,status,linked_to IS NOT NULL,roles FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, tenantID, principalID).Scan(&kind, &status, &linked, &legacy)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotBindable
	}
	if err != nil {
		return err
	}
	if status != "active" || linked {
		return errNotBindable
	}
	if kind == "agent" {
		for _, v := range legacy {
			if v == "system" || v == "importer" || v == "operator" || v == "embedding" || strings.HasPrefix(v, "quote_") {
				return errNotBindable
			}
		}
	}
	return nil
}

type bindingSnapshot struct {
	PrincipalID string   `json:"principal_id"`
	ScopeType   string   `json:"scope_type"`
	ProjectID   string   `json:"project_id"`
	ProjectKey  string   `json:"project_key"`
	Role        *RoleRef `json:"role"`
}

func (m *Module) putProjectMember(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	projectID, principalID := r.PathValue("projectId"), r.PathValue("principal_id")
	if !uuidPattern.MatchString(projectID) || !uuidPattern.MatchString(principalID) {
		apiFail(w, 400, "invalid", "id", "Project and principal IDs must be UUIDs")
		return
	}
	var raw map[string]json.RawMessage
	if err := decode(r, &raw); err != nil {
		apiFail(w, 400, "invalid", "body", err.Error())
		return
	}
	var roleID string
	if value, ok := raw["role_id"]; !ok || len(raw) != 1 || json.Unmarshal(value, &roleID) != nil || !uuidPattern.MatchString(roleID) {
		apiFail(w, 400, "invalid", "role_id", "Supply only role_id, a role UUID")
		return
	}
	var out ProjectBinding
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = m.setProjectBindingTx(r.Context(), tx, p, projectID, principalID, roleID, false)
		return err
	})
	if err != nil {
		projectFail(w, err)
		return
	}
	reply(w, 200, out)
}

// setProjectBindingTx is the common HTTP/operator store path. Callers supply
// the actor principal for both HTTP and operator events.
func (m *Module) setProjectBindingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, principalID, roleID string, operator bool) (ProjectBinding, error) {
	var out ProjectBinding
	projectKey, _, err := projectNodeTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	role, err := roleTx(ctx, tx, roleID)
	if err != nil {
		return out, err
	}
	if !projectRoleAllowed(role) {
		return out, errProjectRole
	}
	if operator {
		if err := lockProjectMutation(ctx, tx, p.TenantID); err != nil {
			return out, err
		}
	} else {
		grants := []string{}
		for _, key := range role.Permissions {
			if ProjectGrantable(key) {
				grants = append(grants, key)
			}
		}
		if err := m.authorizeProjectMutation(ctx, tx, p, projectID, grants); err != nil {
			return out, err
		}
	}
	if err := bindableTx(ctx, tx, p.TenantID, principalID); err != nil {
		return out, err
	}
	var before *bindingSnapshot
	var priorID, priorKey, priorName *string
	err = tx.QueryRow(ctx, `SELECT r.id::text,r.key,r.name FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
		  WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid AND b.scope_type='project' AND b.scope_id=$3::uuid FOR UPDATE OF b`,
		p.TenantID, principalID, projectID).Scan(&priorID, &priorKey, &priorName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if priorID != nil {
		before = &bindingSnapshot{PrincipalID: principalID, ScopeType: "project", ProjectID: projectID, ProjectKey: projectKey, Role: &RoleRef{ID: *priorID, Key: *priorKey, Name: *priorName}}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		  VALUES($1::uuid,$2::uuid,$3::uuid,'project',$4::uuid)
		  ON CONFLICT (tenant_id,principal_id,scope_id) WHERE scope_type='project' DO UPDATE SET role_id=EXCLUDED.role_id
		  RETURNING id::text,created_at`, p.TenantID, principalID, roleID, projectID).Scan(&out.ID, &out.CreatedAt); err != nil {
		return out, err
	}
	out.PrincipalID, out.ProjectID, out.ScopeType = principalID, projectID, "project"
	out.Role = RoleRef{ID: role.ID, Key: role.Key, Name: role.Name}
	if priorID != nil && *priorID == roleID {
		return out, nil
	}
	after := bindingSnapshot{PrincipalID: principalID, ScopeType: "project", ProjectID: projectID, ProjectKey: projectKey, Role: &out.Role}
	return out, appendProjectEvent(ctx, tx, p, projectID, "binding.set", before, after)
}

func (m *Module) deleteProjectMember(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	projectID, principalID := r.PathValue("projectId"), r.PathValue("principal_id")
	if !uuidPattern.MatchString(projectID) || !uuidPattern.MatchString(principalID) {
		apiFail(w, 400, "invalid", "id", "Project and principal IDs must be UUIDs")
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return m.removeProjectBindingTx(r.Context(), tx, p, projectID, principalID, false)
	})
	if err != nil {
		projectFail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}

func (m *Module) removeProjectBindingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, principalID string, operator bool) error {
	projectKey, _, err := projectNodeTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if operator {
		if err := lockProjectMutation(ctx, tx, p.TenantID); err != nil {
			return err
		}
	} else if err := m.authorizeProjectMutation(ctx, tx, p, projectID, nil); err != nil {
		return err
	}
	var roleID, roleKey, roleName string
	err = tx.QueryRow(ctx, `DELETE FROM role_bindings b USING roles r
		  WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid AND b.scope_type='project' AND b.scope_id=$3::uuid
		    AND r.tenant_id=b.tenant_id AND r.id=b.role_id
		  RETURNING r.id::text,r.key,r.name`, p.TenantID, principalID, projectID).Scan(&roleID, &roleKey, &roleName)
	if errors.Is(err, pgx.ErrNoRows) {
		var opens bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM role_bindings b WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid
			  AND b.scope_type='workspace' AND aeon_role_reads_nodes(b.tenant_id,b.role_id,'workspace'))`, p.TenantID, principalID).Scan(&opens); err != nil {
			return err
		}
		if opens {
			return errViaWorkspace
		}
		return errNoSuchBinding
	}
	if err != nil {
		return err
	}
	before := bindingSnapshot{PrincipalID: principalID, ScopeType: "project", ProjectID: projectID, ProjectKey: projectKey, Role: &RoleRef{ID: roleID, Key: roleKey, Name: roleName}}
	return appendProjectEvent(ctx, tx, p, projectID, "binding.removed", before, nil)
}
