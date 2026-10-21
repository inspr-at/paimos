// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type ComputerStartAccess struct {
	ComputerID string                      `json:"computer_id"`
	Admins     bool                        `json:"admins"`
	People     []authz.ComputerStartPerson `json:"people"`
	NextCursor *string                     `json:"next_cursor"`
}

type ComputerAccessBinding struct {
	ID          string        `json:"id"`
	ComputerID  string        `json:"computer_id"`
	PrincipalID string        `json:"principal_id"`
	ScopeType   string        `json:"scope_type"`
	Role        authz.RoleRef `json:"role"`
}

func computerAccessTarget(ctx context.Context, tx pgx.Tx, computer string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent_pairing_computers WHERE id=$1 AND archived_at IS NULL`, computer).Scan(&id)
	return notFound(err)
}

func (m *Module) computerAccess(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	computer, cursor := r.PathValue("computerId"), r.URL.Query().Get("cursor")
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			WriteError(w, fail(400, "invalid", "limit must be between 1 and 100"))
			return
		}
	}
	if !uuidRE.MatchString(computer) || cursor != "" && !uuidRE.MatchString(cursor) {
		WriteError(w, fail(400, "invalid", "computer and cursor must be UUIDs"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := ComputerStartAccess{ComputerID: computer, Admins: true}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}); err != nil {
			return err
		}
		if err := computerAccessTarget(ctx, tx, computer); err != nil {
			return err
		}
		var err error
		out.People, out.NextCursor, err = authz.ComputerStartPeopleTx(ctx, tx, p.TenantID, computer, cursor, limit)
		return err
	})
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			err = fail(403, "forbidden", "account.read required")
		}
		WriteError(w, err)
		return
	}
	reply(w, out)
}

func (m *Module) putComputerAccess(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		RoleID string `json:"role_id"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if !uuidRE.MatchString(in.RoleID) {
		WriteError(w, fail(400, "invalid", "role_id must be a UUID"))
		return
	}
	m.writeComputerAccess(w, r, p, in.RoleID)
}

func (m *Module) deleteComputerAccess(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	m.writeComputerAccess(w, r, p, "")
}

func (m *Module) writeComputerAccess(w http.ResponseWriter, r *http.Request, p tenant.Principal, roleID string) {
	computer, person := r.PathValue("computerId"), r.PathValue("principal_id")
	if !uuidRE.MatchString(computer) || !uuidRE.MatchString(person) {
		WriteError(w, fail(400, "invalid", "computer and person must be UUIDs"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := ComputerAccessBinding{ComputerID: computer, PrincipalID: person, ScopeType: "computer"}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		// Same authority as project-access grants, rechecked under the common
		// tenant/tree/pairing fence retained by every final claim transaction.
		if err := authz.RequireTx(ctx, tx, p, "members.manage", authz.Scope{}); err != nil {
			return err
		}
		if err := computerAccessTarget(ctx, tx, computer); err != nil {
			return err
		}
		var bindable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND linked_to IS NULL AND ($3::bool OR status='active'))`, p.TenantID, person, roleID == "").Scan(&bindable); err != nil {
			return err
		}
		if !bindable {
			return fail(400, "invalid", "only active people can be granted computer access")
		}
		var priorRole *string
		if err := tx.QueryRow(ctx, `SELECT (SELECT role_id::text FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='computer' AND computer_id=$3)`, p.TenantID, person, computer).Scan(&priorRole); err != nil {
			return err
		}
		before := map[string]any{"computer_id": computer, "principal_id": person, "scope_type": "computer", "role_id": priorRole}
		if roleID == "" {
			if priorRole == nil {
				return fail(404, "not_found", "no computer grant to remove; workspace access is unchanged")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='computer' AND computer_id=$3`, p.TenantID, person, computer); err != nil {
				return err
			}
			return appendEvent(ctx, tx, p, events.Change{Type: "agent_pairing.computer_access_revoked", Before: before})
		}
		var builtin, grants bool
		if err := tx.QueryRow(ctx, `SELECT id::text,key,name,builtin,
 EXISTS(SELECT 1 FROM role_permissions WHERE tenant_id=roles.tenant_id AND role_id=roles.id AND permission=$2)
 FROM roles WHERE id=$1`, roleID, authz.StartOnComputer).Scan(&out.Role.ID, &out.Role.Key, &out.Role.Name, &builtin, &grants); err != nil {
			return notFound(err)
		}
		if builtin {
			permissions, _ := authz.BuiltinPermissions(out.Role.Key)
			grants = false
			for _, permission := range permissions {
				if permission == authz.StartOnComputer {
					grants = true
				}
			}
		}
		if !grants || builtin && (out.Role.Key == "owner" || out.Role.Key == "customer") {
			return fail(400, "invalid", "choose a role with computer start permission; Owner is workspace-only")
		}
		allowed, err := authz.CanStartOnComputerTx(ctx, tx, p, computer)
		if err != nil {
			return err
		}
		if !allowed {
			return authz.ErrForbidden
		}
		if err := tx.QueryRow(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,computer_id)
 VALUES($1,$2,$3,'computer',$4) ON CONFLICT(tenant_id,principal_id,computer_id) WHERE scope_type='computer'
 DO UPDATE SET role_id=EXCLUDED.role_id RETURNING id::text`, p.TenantID, person, roleID, computer).Scan(&out.ID); err != nil {
			return err
		}
		if priorRole != nil && *priorRole == roleID {
			return nil
		}
		return appendEvent(ctx, tx, p, events.Change{Type: "agent_pairing.computer_access_granted", Before: before, After: out})
	})
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			err = fail(403, "forbidden", "members.manage and authority to grant this computer access required")
		}
		WriteError(w, err)
		return
	}
	if roleID == "" {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	reply(w, out)
}
