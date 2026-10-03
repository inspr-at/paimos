// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type agentCreate struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	WorkspaceRoleID string `json:"workspace_role_id"`
	ProjectRoles    []struct {
		ProjectID string `json:"project_id"`
		RoleID    string `json:"role_id"`
	} `json:"project_roles"`
}

var errAgentNameTaken = errors.New("agent name already in use")
var errAgentRole = errors.New("role is not available for agents in this scope")

func (m *Module) createAgent(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	if p.Kind != tenant.Person {
		apiFail(w, 403, "forbidden", "", "Only people may create agents")
		return
	}
	var body agentCreate
	if err := decode(r, &body); err != nil {
		apiFail(w, 400, "invalid", "body", "Invalid agent details")
		return
	}
	body.Name, body.Description = strings.TrimSpace(body.Name), strings.TrimSpace(body.Description)
	if body.Name == "" || utf8.RuneCountInString(body.Name) > 200 || strings.ContainsAny(body.Name, "\x00\r\n") {
		apiFail(w, 400, "invalid", "name", "Enter a name of at most 200 characters")
		return
	}
	if utf8.RuneCountInString(body.Description) > 1000 || strings.ContainsRune(body.Description, 0) {
		apiFail(w, 400, "invalid", "description", "Description must be at most 1000 characters")
		return
	}
	if body.WorkspaceRoleID == "" && len(body.ProjectRoles) == 0 || body.WorkspaceRoleID != "" && !uuidPattern.MatchString(body.WorkspaceRoleID) || len(body.ProjectRoles) > 50 {
		apiFail(w, 400, "invalid", "workspace_role_id", "Choose a workspace role or up to 50 projects")
		return
	}
	seen := map[string]bool{}
	for _, pr := range body.ProjectRoles {
		if !uuidPattern.MatchString(pr.ProjectID) || !uuidPattern.MatchString(pr.RoleID) || seen[strings.ToLower(pr.ProjectID)] {
			apiFail(w, 400, "invalid", "project_roles", "Choose each project once, with a valid role")
			return
		}
		seen[strings.ToLower(pr.ProjectID)] = true
	}
	out := AgentMember{Name: body.Name, Description: body.Description, ProjectRoles: []ProjectRole{}}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		// Serialize with role edits, demotion and legacy named-key creation.
		if err := LockProjectMutation(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := requireTx(ctx, tx, p, "keys.manage", Scope{}); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND lower(name)=lower($2))`, p.TenantID, body.Name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errAgentNameTaken
		}
		checkRole := func(id string, project bool) (Role, error) {
			role, err := roleTx(ctx, tx, id)
			if err != nil {
				return role, err
			}
			if role.Builtin && (role.Key == "owner" || role.Key == "customer" || !project && role.Key == "guest") {
				return role, errAgentRole
			}
			// As with invites, require the creator to hold the role's full grant in
			// the workspace. keys.manage cannot be used to delegate other authority.
			return role, canGrantTx(ctx, tx, p, role.Permissions)
		}
		if body.WorkspaceRoleID != "" {
			role, err := checkRole(body.WorkspaceRoleID, false)
			if err != nil {
				return err
			}
			out.WorkspaceRole = &RoleRef{ID: role.ID, Key: role.Key, Name: role.Name}
		}
		for _, pr := range body.ProjectRoles {
			role, err := checkRole(pr.RoleID, true)
			if err != nil {
				return err
			}
			key, title, err := projectNodeTx(ctx, tx, pr.ProjectID)
			if err != nil {
				return err
			}
			// Seeing the project is required even if the caller has keys.manage.
			if err := requireTx(ctx, tx, p, "nodes.read", Scope{ProjectID: pr.ProjectID}); err != nil {
				return err
			}
			out.ProjectRoles = append(out.ProjectRoles, ProjectRole{ProjectID: pr.ProjectID, ProjectKey: key, ProjectTitle: title, Role: RoleRef{ID: role.ID, Key: role.Key, Name: role.Name}})
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,description,roles,agent_access_configured)
   VALUES($1::uuid,'agent',$2,$3,'{}',true) RETURNING id::text`, p.TenantID, out.Name, out.Description).Scan(&out.PrincipalID); err != nil {
			return err
		}
		if err := appendEvent(ctx, tx, p, "principal.agent_created", nil, map[string]any{"principal_id": out.PrincipalID, "name": out.Name, "description": out.Description}); err != nil {
			return err
		}
		if out.WorkspaceRole != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, p.TenantID, out.PrincipalID, out.WorkspaceRole.ID); err != nil {
				return err
			}
			if err := appendEvent(ctx, tx, p, "binding.set", nil, map[string]any{"principal_id": out.PrincipalID, "scope_type": "workspace", "role": out.WorkspaceRole}); err != nil {
				return err
			}
		}
		for _, pr := range out.ProjectRoles {
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1::uuid,$2::uuid,$3::uuid,'project',$4::uuid)`, p.TenantID, out.PrincipalID, pr.Role.ID, pr.ProjectID); err != nil {
				return err
			}
			if err := appendProjectEvent(ctx, tx, p, pr.ProjectID, "binding.set", nil, bindingSnapshot{PrincipalID: out.PrincipalID, ScopeType: "project", ProjectID: pr.ProjectID, ProjectKey: pr.ProjectKey, Role: &pr.Role}); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errAgentNameTaken):
		apiFail(w, 409, "name_taken", "name", "This name is already in use; choose another name")
	case errors.Is(err, errAgentRole):
		apiFail(w, 400, "invalid", "workspace_role_id", "Choose an agent role available in this scope")
	case err != nil:
		projectFail(w, err)
	default:
		reply(w, http.StatusCreated, out)
	}
}

// AgentKeyCeilingTx includes project-only grants without granting workspace
// access. Actual requests still check the specific project's bindings and the
// key creator's live authority. This also caps editing and rotation.
func AgentKeyCeilingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]string, error) {
	effective, err := loadTx(ctx, tx, p, "")
	if err != nil {
		return nil, err
	}
	return effective.anyProject, nil
}
