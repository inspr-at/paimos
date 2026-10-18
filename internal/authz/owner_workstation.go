// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"errors"
	"slices"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// OwnerWorkstationPermission is deliberately separate from AgentGrantable.
// Ordinary agent keys retain their existing permission catalog and boundary.
func OwnerWorkstationPermission(permission string) bool {
	switch permission {
	case "members.manage", "roles.manage", "keys.read", "keys.manage", "settings.manage", "audit.read",
		"harness.watch", "harness.force_stop", "harness.recover", "rules.publish", "approvals.decide", "approvals.decide_high":
		return true
	}
	return false
}

// OwnerWorkstation identifies an authenticated designation, not a person. It
// never replaces Require/RequireTx, which validate the current key and pairing.
func OwnerWorkstation(p tenant.Principal) bool {
	return p.Kind == tenant.Agent && p.OwnerWorkstation && p.KeyID != "" && p.WorkstationComputerID != ""
}

func KeyGrantable(permission string, marked bool) bool {
	perm, ok := Lookup(permission)
	return ok && (perm.AgentGrantable || marked && OwnerWorkstationPermission(permission))
}

// WorkstationKeyTx rechecks the key's exact tenant, principal, computer and
// pairing; reported daemon capability or a caller-supplied public key is never
// authority. Scopes and full_access are re-read in this transaction, not taken
// from the authentication snapshot. The stored list remains the historical
// snapshot; callers expand a full-access key with ResolveKeyScopes.
func WorkstationKeyTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, []string, bool, error) {
	if !OwnerWorkstation(p) {
		return "", nil, false, ErrForbidden
	}
	var public string
	var scopes []string
	var fullAccess bool
	err := tx.QueryRow(ctx, `SELECT c.local_auth_public_key,k.scopes,coalesce(k.full_access,false)
	 FROM agent_keys k JOIN agent_pairing_computers c ON c.tenant_id=k.tenant_id AND c.id=k.workstation_computer_id AND c.principal_id=k.principal_id
	 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
	 JOIN principals a ON a.tenant_id=k.tenant_id AND a.id=k.principal_id
	 WHERE k.tenant_id=$1::uuid AND k.id=$2::uuid AND k.principal_id=$3::uuid AND c.id=$4::uuid
	 AND k.owner_workstation AND k.workstation_generation=$5 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp())
	 AND c.state='connected' AND q.state='redeemed' AND c.local_auth_public_key<>'' AND a.kind='agent' AND a.status='active'`,
		p.TenantID, p.KeyID, p.ID, p.WorkstationComputerID, p.WorkstationGeneration).Scan(&public, &scopes, &fullAccess)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrForbidden
	}
	return public, scopes, fullAccess, err
}

func workstationEffectiveTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, result *Effective) error {
	if !OwnerWorkstation(p) {
		return nil
	}
	_, scopes, fullAccess, err := WorkstationKeyTx(ctx, tx, p)
	if err != nil {
		return err
	}
	// Full access follows the live agent-grantable catalog. ResolveKeyScopes
	// ignores the stored snapshot, so the step-up set is not implied.
	scopes = ResolveKeyScopes(scopes, fullAccess)
	// The explicit mark is the owner's opt-in for conversation viewing. Ordinary
	// Owner/Admin roles still do not implicitly receive harness.watch.
	watch, err := WorkstationWatchCeilingTx(ctx, tx, p)
	if err != nil {
		return err
	}
	if watch && containsScope(scopes, "harness.watch") {
		result.Workspace.Permissions = append(result.Workspace.Permissions, "harness.watch")
		result.anyProject = append(result.anyProject, "harness.watch")
		if result.Project != nil {
			result.Project.Permissions = append(result.Project.Permissions, "harness.watch")
		}
	}
	filter := func(perms []string) []string {
		return slices.DeleteFunc(perms, func(permission string) bool {
			return !KeyGrantable(permission, true) || !containsScope(scopes, permission)
		})
	}
	result.Workspace.Permissions = filter(result.Workspace.Permissions)
	result.anyProject = filter(result.anyProject)
	if result.Project != nil {
		result.Project.Permissions = filter(result.Project.Permissions)
	}
	return nil
}

// The owner designation is explicit watch consent only within both live role
// ceilings. A later creator demotion must remove it as it removes other grants.
func WorkstationWatchCeilingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (bool, error) {
	eligible := func(g grants) bool {
		return g.allows("harness.watch", "") || g.workspaceRole != nil && (g.workspaceRole.Key == "owner" || g.workspaceRole.Key == "admin")
	}
	own, err := readGrants(ctx, tx, p)
	if err != nil {
		return false, err
	}
	if !eligible(own) {
		return false, nil
	}
	if p.KeyCreatorID != "" {
		creator, err := readGrants(ctx, tx, tenant.Principal{ID: p.KeyCreatorID, TenantID: p.TenantID, Kind: tenant.Person})
		if err != nil {
			return false, err
		}
		return eligible(creator), nil
	}
	return true, nil
}
