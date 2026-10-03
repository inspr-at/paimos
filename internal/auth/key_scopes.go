// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type keyScopeDelta struct {
	Add           []string          `json:"add"`
	Remove        []string          `json:"remove"`
	RoleExtension *keyRoleExtension `json:"role_extension,omitempty"`
	// RawMessage distinguishes an omitted expiry from an explicit null (Never).
	ExpiresAt json.RawMessage `json:"expires_at,omitempty"`
}

type keyRoleExtension struct {
	RoleID string   `json:"role_id"`
	Add    []string `json:"add"`
}

type keyScopeView struct {
	Key           agentKeyJSON `json:"key"`
	Grantable     []string     `json:"grantable_scopes"`
	AgentRole     *authz.Role  `json:"agent_role"`
	RoleGrantable []string     `json:"role_grantable_scopes"`
}

var errKeyInactive = errors.New("key is revoked or expired")
var errKeyScopes = errors.New("invalid scopes")
var errKeyExpiry = errors.New("expires_at must be null or a future timestamp")

func (m *Module) handleAgentKeyScopes(w http.ResponseWriter, r *http.Request) {
	p, ok := m.requireKeyManagement(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid id")
		return
	}
	var delta *keyScopeDelta
	if r.Method == http.MethodPatch {
		delta = &keyScopeDelta{}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(delta); err != nil {
			writeBadRequest(w, "invalid JSON")
			return
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			writeBadRequest(w, "expected one JSON object")
			return
		}
		if delta.Add == nil && delta.Remove == nil && delta.ExpiresAt == nil {
			writeBadRequest(w, "add, remove or expires_at is required")
			return
		}
	}
	view, err := m.agentKeyScopes(r.Context(), p, id, delta)
	switch {
	case errors.Is(err, authz.ErrForbidden):
		writeForbidden(w)
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, errorJSON{Error: "key not found"})
	case errors.Is(err, errKeyInactive):
		writeJSON(w, http.StatusConflict, errorJSON{Error: errKeyInactive.Error()})
	case errors.Is(err, errKeyScopes):
		writeBadRequest(w, "invalid scopes")
	case errors.Is(err, errKeyExpiry):
		writeBadRequest(w, errKeyExpiry.Error())
	case err != nil:
		writeInternal(w)
	case delta != nil:
		writeJSON(w, http.StatusOK, view.Key)
	default:
		writeJSON(w, http.StatusOK, view)
	}
}

// Normalize every delta at the transaction entry point, including internal callers.
func normalizeKeyScopeDelta(delta *keyScopeDelta) (*keyScopeDelta, error) {
	if delta == nil {
		return nil, nil
	}
	if len(delta.ExpiresAt) > 128 {
		return nil, errKeyExpiry
	}
	out := &keyScopeDelta{ExpiresAt: slices.Clone(delta.ExpiresAt)}
	if out.ExpiresAt != nil {
		if _, err := keyScopeExpiry(out.ExpiresAt, time.Now()); err != nil {
			return nil, err
		}
	}
	var err error
	out.Add, err = cleanScopes(delta.Add)
	if err != nil || len(delta.Remove) > maxScopeInput {
		return nil, errKeyScopes
	}
	for _, scope := range delta.Remove {
		scope = strings.ReplaceAll(strings.TrimSpace(scope), ":", ".")
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n\x00") || slices.Contains(out.Add, scope) {
			return nil, errKeyScopes
		}
		out.Remove = append(out.Remove, scope)
	}
	if extension := delta.RoleExtension; extension != nil {
		add, err := cleanScopes(extension.Add)
		if err != nil || len(add) == 0 || !uuidRe.MatchString(extension.RoleID) {
			return nil, errKeyScopes
		}
		for _, scope := range add {
			if !slices.Contains(out.Add, scope) {
				return nil, errKeyScopes
			}
		}
		out.RoleExtension = &keyRoleExtension{RoleID: extension.RoleID, Add: add}
	}
	return out, nil
}

func keyScopeExpiry(raw json.RawMessage, now time.Time) (*time.Time, error) {
	var expires *time.Time
	if err := json.Unmarshal(raw, &expires); err != nil || expires != nil && !expires.After(now) {
		return nil, errKeyExpiry
	}
	return expires, nil
}

func (m *Module) agentKeyScopes(ctx context.Context, p tenant.Principal, id string, delta *keyScopeDelta) (keyScopeView, error) {
	view := keyScopeView{Grantable: []string{}, RoleGrantable: []string{}}
	if p.Kind != tenant.Person || p.ID == "" {
		return view, authz.ErrForbidden
	}
	delta, err := normalizeKeyScopeDelta(delta)
	if err != nil {
		return view, err
	}
	err = m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Match role management and rotation: tenant first, then key. All live
		// grants, role extensions, cleanup and audit share this transaction.
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&locked); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "keys.manage", authz.Scope{}); err != nil {
			return err
		}
		key, err := lockAgentKey(ctx, tx, id)
		if err != nil {
			return err
		}
		if key.RevokedAt != nil || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
			return errKeyInactive
		}
		before := keySnapshot(key)
		expires := key.ExpiresAt
		if delta != nil && delta.ExpiresAt != nil {
			// Check again under the fence: a timestamp can expire while waiting.
			expires, err = keyScopeExpiry(delta.ExpiresAt, time.Now())
			if err != nil {
				return err
			}
		}
		var creator *string
		if err := tx.QueryRow(ctx, `SELECT created_by_principal_id::text FROM agent_keys WHERE id=$1::uuid`, id).Scan(&creator); err != nil {
			return err
		}
		agent := tenant.Principal{ID: key.PrincipalID, TenantID: p.TenantID, Kind: tenant.Agent}
		if creator != nil {
			agent.KeyCreatorID = *creator
		}
		role, err := agentKeyRoleTx(ctx, tx, agent)
		if err != nil {
			return err
		}
		view.AgentRole = role
		editor, err := authz.EffectiveTx(ctx, tx, p, "")
		if err != nil {
			return err
		}
		creatorPermissions := editor.Workspace.Permissions
		if creator != nil && *creator != p.ID {
			originalCreator, err := authz.EffectiveTx(ctx, tx, tenant.Principal{ID: *creator, TenantID: p.TenantID, Kind: tenant.Person}, "")
			if err != nil {
				return err
			}
			creatorPermissions = originalCreator.Workspace.Permissions
		}
		if creator == nil {
			// Legacy operator keys have no original-creator cap.
			creatorPermissions = nil
			for _, perm := range authz.Registry {
				creatorPermissions = append(creatorPermissions, perm.Key)
			}
		}
		roleChanged := false
		if delta != nil && delta.RoleExtension != nil {
			extension := delta.RoleExtension
			if err := authz.RequireTx(ctx, tx, p, "roles.manage", authz.Scope{}); err != nil {
				return err
			}
			// Never accept a different role, even one in the same tenant. A stale
			// sheet must not extend the agent's newly assigned role by accident.
			if role == nil || role.Builtin || !strings.EqualFold(role.ID, extension.RoleID) {
				return authz.ErrForbidden
			}
			roleBefore := *role
			roleBefore.Permissions = slices.Clone(role.Permissions)
			for _, scope := range extension.Add {
				if !slices.Contains(editor.Workspace.Permissions, scope) || !slices.Contains(creatorPermissions, scope) {
					return authz.ErrForbidden
				}
				if slices.Contains(role.Permissions, scope) {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3) ON CONFLICT DO NOTHING`, p.TenantID, role.ID, scope); err != nil {
					return err
				}
				role.Permissions = append(role.Permissions, scope)
				roleChanged = true
			}
			if roleChanged {
				before["role"] = roleBefore
			}
		}
		ceiling, err := authz.AgentKeyCeilingTx(ctx, tx, agent)
		if err != nil {
			return err
		}
		mayManageRoles := slices.Contains(editor.Workspace.Permissions, "roles.manage")
		for _, perm := range authz.Registry {
			if !perm.AgentGrantable || !slices.Contains(editor.Workspace.Permissions, perm.Key) {
				continue
			}
			if slices.Contains(ceiling, perm.Key) {
				view.Grantable = append(view.Grantable, perm.Key)
			} else if mayManageRoles && role != nil && !role.Builtin && !slices.Contains(role.Permissions, perm.Key) && slices.Contains(creatorPermissions, perm.Key) {
				view.RoleGrantable = append(view.RoleGrantable, perm.Key)
			}
		}
		// Clean retired registry entries on this management read and on edits.
		// Keep known scopes, including temporarily ungrantable ones, until the
		// editor explicitly removes them. Never invent replacement permissions.
		after := []string{}
		pruned := []string{}
		for _, stored := range key.Scopes {
			scope := strings.ReplaceAll(stored, ":", ".")
			if _, known := authz.Lookup(scope); !known {
				pruned = append(pruned, stored)
				continue
			}
			if delta != nil && slices.Contains(delta.Remove, scope) {
				continue
			}
			if !slices.Contains(after, scope) {
				after = append(after, scope)
			}
		}
		if delta != nil {
			for _, scope := range delta.Add {
				if !slices.Contains(after, scope) {
					after = append(after, scope)
				}
			}
			if len(after) > maxScopeInput {
				return errKeyScopes
			}
			for _, scope := range after {
				if !slices.Contains(view.Grantable, scope) {
					return authz.ErrForbidden
				}
			}
		}
		expiryChanged := (key.ExpiresAt == nil) != (expires == nil) || key.ExpiresAt != nil && expires != nil && !key.ExpiresAt.Equal(*expires)
		if roleChanged || expiryChanged || !slices.Equal(key.Scopes, after) {
			if _, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=$2::text[],expires_at=$3 WHERE id=$1::uuid`, id, after, expires); err != nil {
				return err
			}
			key.Scopes = after
			key.ExpiresAt = expires
			auditAfter := keySnapshot(key)
			if roleChanged {
				auditAfter["role"] = role
			}
			if len(pruned) > 0 {
				auditAfter["pruned_scopes"] = pruned
			}
			if _, err := events.Append(ctx, tx, p, events.Change{Type: "agent_key.scopes_changed", Before: before, After: auditAfter}); err != nil {
				return err
			}
		}
		view.Key = keyJSON(key)
		return nil
	})
	return view, err
}

func agentKeyRoleTx(ctx context.Context, tx pgx.Tx, agent tenant.Principal) (*authz.Role, error) {
	var role authz.Role
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.key,r.name,r.description,r.builtin,r.based_on::text,
		(SELECT count(DISTINCT other.principal_id) FROM role_bindings other WHERE other.tenant_id=r.tenant_id AND other.role_id=r.id)
		FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
		WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid AND b.scope_type='workspace'`, agent.TenantID, agent.ID).Scan(&role.ID, &role.Key, &role.Name, &role.Description, &role.Builtin, &role.BasedOn, &role.MemberCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if role.Builtin {
		role.Permissions, _ = authz.BuiltinPermissions(role.Key)
		return &role, nil
	}
	role.Permissions = []string{}
	rows, err := tx.Query(ctx, `SELECT permission FROM role_permissions WHERE tenant_id=$1::uuid AND role_id=$2::uuid ORDER BY permission`, agent.TenantID, role.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		role.Permissions = append(role.Permissions, permission)
	}
	return &role, rows.Err()
}
