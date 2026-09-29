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

type Alias struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
	Source      string `json:"source"`
}
type ProjectRole struct {
	ProjectID    string  `json:"project_id"`
	ProjectKey   string  `json:"project_key"`
	ProjectTitle string  `json:"project_title"`
	Role         RoleRef `json:"role"`
}
type Member struct {
	PrincipalID   string        `json:"principal_id"`
	Name          string        `json:"name"`
	AvatarURL     *string       `json:"avatar_url"`
	HasAvatar     bool          `json:"has_avatar"`
	Email         *string       `json:"email"`
	Status        string        `json:"status"`
	Identity      *string       `json:"identity"`
	WorkspaceRole *RoleRef      `json:"workspace_role"`
	ProjectRoles  []ProjectRole `json:"project_roles"`
	Aliases       []Alias       `json:"aliases"`
	ClassicRole   *string       `json:"classic_role"`
	LastActiveAt  *time.Time    `json:"last_active_at"`
	LastOwner     bool          `json:"last_owner"`
}
type AgentMember struct {
	Description   string        `json:"description"`
	ProjectRoles  []ProjectRole `json:"project_roles"`
	PrincipalID   string        `json:"principal_id"`
	Name          string        `json:"name"`
	HasAvatar     bool          `json:"has_avatar"`
	WorkspaceRole *RoleRef      `json:"workspace_role"`
	KeyCount      int           `json:"key_count"`
	LastSeenAt    *time.Time    `json:"last_seen_at"`
	Service       bool          `json:"service"`
	Preview       AgentPreview  `json:"preview,omitzero"`
}
type ImportedMember struct {
	PrincipalID string  `json:"principal_id"`
	Name        string  `json:"name"`
	ClassicRole *string `json:"classic_role"`
}
type MemberDirectory struct {
	People      []Member               `json:"people"`
	Agents      []AgentMember          `json:"agents"`
	Invites     []Invite               `json:"invites"`
	Imported    []ImportedMember       `json:"imported"`
	OwnerCount  int                    `json:"owner_count"`
	Provisioner *provisionerCapability `json:"provisioner"`
}
type provisionerCapability struct {
	Name string `json:"name"`
}

func (m *Module) members(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	out := MemberDirectory{People: []Member{}, Agents: []AgentMember{}, Invites: []Invite{}, Imported: []ImportedMember{}}
	if provisioner := m.provisionerFor(p.TenantID); provisioner != nil {
		out.Provisioner = &provisionerCapability{Name: provisioner.Name()}
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT p.id::text,p.kind,p.name,p.description,p.email,p.status,p.roles,
          i.issuer,coalesce(pp.avatar_original_hash,''),br.id::text,br.key,br.name,
          (SELECT max(s.last_seen_at) FROM sessions s WHERE s.tenant_id=p.tenant_id AND s.principal_id=p.id),
          (SELECT count(*) FROM agent_keys k WHERE k.tenant_id=p.tenant_id AND k.principal_id=p.id AND k.revoked_at IS NULL),
          (SELECT max(k.last_used_at) FROM agent_keys k WHERE k.tenant_id=p.tenant_id AND k.principal_id=p.id),
          EXISTS (SELECT 1 FROM personal_profiles avatar WHERE avatar.tenant_id=p.tenant_id AND avatar.principal_id=p.id AND avatar.avatar_hashes <> '{}'::jsonb)
          FROM principals p
          LEFT JOIN identities i ON i.id=p.identity_id
          LEFT JOIN personal_profiles pp ON pp.tenant_id=p.tenant_id AND pp.principal_id=p.id
          LEFT JOIN role_bindings b ON b.tenant_id=p.tenant_id AND b.principal_id=p.id AND b.scope_type='workspace'
          LEFT JOIN roles br ON br.tenant_id=b.tenant_id AND br.id=b.role_id
          WHERE p.tenant_id=$1::uuid ORDER BY p.kind DESC,p.name,p.id`, p.TenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, kind, name, description, status, avatarHash string
			var email, issuer, roleID, roleKey, roleName *string
			var legacy []string
			var lastActive, lastSeen *time.Time
			var keyCount int
			var hasAvatar bool
			if err := rows.Scan(&id, &kind, &name, &description, &email, &status, &legacy, &issuer, &avatarHash, &roleID, &roleKey, &roleName, &lastActive, &keyCount, &lastSeen, &hasAvatar); err != nil {
				rows.Close()
				return err
			}
			var role *RoleRef
			if roleID != nil {
				role = &RoleRef{ID: *roleID, Key: *roleKey, Name: *roleName}
			}
			if kind == "agent" {
				service := false
				for _, v := range legacy {
					if v == "system" || v == "importer" || v == "operator" || v == "embedding" || strings.HasPrefix(v, "quote_") {
						service = true
					}
				}
				out.Agents = append(out.Agents, AgentMember{PrincipalID: id, Name: name, Description: description, ProjectRoles: []ProjectRole{}, HasAvatar: hasAvatar, WorkspaceRole: role, KeyCount: keyCount, LastSeenAt: lastSeen, Service: service})
				continue
			}
			var classic *string
			if len(legacy) > 0 {
				v := legacy[0]
				classic = &v
			}
			var identity *string
			if issuer != nil && *issuer != "paimos-classic" {
				v := "inspr_id"
				identity = &v
			}
			var avatar *string
			if avatarHash != "" {
				v := "/api/people/" + id + "/avatar"
				avatar = &v
			}
			item := Member{PrincipalID: id, Name: name, AvatarURL: avatar, HasAvatar: hasAvatar, Email: email, Status: status, Identity: identity, WorkspaceRole: role, ProjectRoles: []ProjectRole{}, Aliases: []Alias{}, ClassicRole: classic, LastActiveAt: lastActive}
			out.People = append(out.People, item)
			if issuer != nil && *issuer == "paimos-classic" {
				out.Imported = append(out.Imported, ImportedMember{PrincipalID: id, Name: name, ClassicRole: classic})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		aliases, err := tx.Query(r.Context(), `SELECT linked_to::text,id::text,name FROM principals WHERE tenant_id=$1::uuid AND linked_to IS NOT NULL ORDER BY name,id`, p.TenantID)
		if err != nil {
			return err
		}
		for aliases.Next() {
			var target, source, name string
			if err := aliases.Scan(&target, &source, &name); err != nil {
				aliases.Close()
				return err
			}
			for i := range out.People {
				if out.People[i].PrincipalID == target {
					out.People[i].Aliases = append(out.People[i].Aliases, Alias{PrincipalID: source, Name: name, Source: "classic"})
					break
				}
			}
		}
		err = aliases.Err()
		aliases.Close()
		if err != nil {
			return err
		}
		out.OwnerCount, err = ownerCount(r.Context(), tx, p.TenantID)
		if err != nil {
			return err
		}
		if err = fillAgentPreviews(r.Context(), tx, p, out.Agents); err != nil {
			return err
		}
		applyOwnerFlags(out.People, out.OwnerCount)
		if err := attachProjectRoles(r.Context(), tx, p.TenantID, out.People); err != nil {
			return err
		}
		agentMembers := make([]Member, len(out.Agents))
		for i, a := range out.Agents {
			agentMembers[i] = Member{PrincipalID: a.PrincipalID, ProjectRoles: []ProjectRole{}}
		}
		if err := attachProjectRoles(r.Context(), tx, p.TenantID, agentMembers); err != nil {
			return err
		}
		for i := range out.Agents {
			out.Agents[i].ProjectRoles = agentMembers[i].ProjectRoles
		}
		out.Invites, err = listInvites(r.Context(), tx, p.TenantID)
		return err
	})
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 200, out)
}

// errGuestWorkspace: Guest is a project-only role (ADR-003 P2).
var errGuestWorkspace = errors.New("guest is a project role")
var errOperatorOwner = errors.New("operator cannot grant the owner role")

func (m *Module) putWorkspaceRole(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("principal_id")
	if !uuidPattern.MatchString(id) {
		apiFail(w, 400, "invalid", "principal_id", "Principal ID must be a UUID")
		return
	}
	var raw map[string]json.RawMessage
	if err := decode(r, &raw); err != nil {
		apiFail(w, 400, "invalid", "body", err.Error())
		return
	}
	value, ok := raw["role_id"]
	if !ok || len(raw) != 1 {
		apiFail(w, 400, "invalid", "role_id", "Supply only role_id")
		return
	}
	var roleID *string
	if string(value) != "null" {
		var idValue string
		if err := json.Unmarshal(value, &idValue); err != nil || !uuidPattern.MatchString(idValue) {
			apiFail(w, 400, "invalid", "role_id", "Role ID must be a UUID or null")
			return
		}
		roleID = &idValue
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return m.setWorkspaceRoleTx(r.Context(), tx, p, id, roleID, false)
	})
	if errors.Is(err, errAliasTarget) {
		apiFail(w, 409, "conflict", "principal_id", "An alias uses the person's role")
		return
	}
	if errors.Is(err, errGuestWorkspace) {
		apiFail(w, 400, "project_only_role", "role_id", "Guest is a project role; grant it on a project instead")
		return
	}
	if err != nil {
		internalFail(w, err)
		return
	}
	// The directory shape is shared with GET /members. Return the changed row.
	var result Member
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		result, err = readMember(r.Context(), tx, p.TenantID, id)
		return err
	})
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 200, result)
}

// setWorkspaceRoleTx is shared by the HTTP handler and host-only CLI. The
// operator bypasses caller permissions, but cannot mint an owner binding.
func (m *Module) setWorkspaceRoleTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, roleID *string, operator bool) error {
	if operator {
		if err := lockProjectMutation(ctx, tx, p.TenantID); err != nil {
			return err
		}
	} else if err := m.authorizeMutation(ctx, tx, p, "members.manage", nil); err != nil {
		return err
	}
	if roleID != nil {
		targetRole, err := roleTx(ctx, tx, *roleID)
		if err != nil {
			return err
		}
		if targetRole.Builtin && targetRole.Key == "guest" {
			return errGuestWorkspace
		}
		if targetRole.Key == "owner" {
			if operator {
				return errOperatorOwner
			}
			if err := requireTx(ctx, tx, p, "ownership.transfer", Scope{}); err != nil {
				return err
			}
		}
		if !operator {
			if err := canGrantTx(ctx, tx, p, targetRole.Permissions); err != nil {
				return err
			}
		}
	}
	var kind, status string
	var legacy []string
	var linked *string
	if err := tx.QueryRow(ctx, `SELECT kind,status,roles,linked_to::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, p.TenantID, id).Scan(&kind, &status, &legacy, &linked); err != nil {
		return err
	}
	if linked != nil {
		return errAliasTarget
	}
	if status != "active" {
		return ErrForbidden
	}
	if kind == "agent" {
		for _, v := range legacy {
			if v == "system" || v == "importer" || v == "operator" || v == "embedding" || strings.HasPrefix(v, "quote_") {
				return ErrForbidden
			}
		}
	}
	var priorID *string
	var priorKey *string
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.key FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid AND b.scope_type='workspace' FOR UPDATE OF b`, p.TenantID, id).Scan(&priorID, &priorKey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if priorKey != nil && *priorKey == "owner" && !operator {
		if err := requireTx(ctx, tx, p, "ownership.transfer", Scope{}); err != nil {
			return err
		}
	}
	if priorID != nil && roleID != nil && *priorID == *roleID {
		return nil
	}
	if roleID == nil {
		if priorID == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, p.TenantID, id); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace') ON CONFLICT (tenant_id,principal_id) WHERE scope_type='workspace' DO UPDATE SET role_id=EXCLUDED.role_id`, p.TenantID, id, *roleID); err != nil {
			return err
		}
	}
	return appendEvent(ctx, tx, p, "authz.workspace_role_changed", map[string]any{"principal_id": id, "role_id": priorID}, map[string]any{"principal_id": id, "role_id": roleID})
}
