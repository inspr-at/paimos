// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "github.com/inspr-at/paimos/internal/tenant"

// ResolveKeyScopes expands only in memory, using the live catalog. A full-access
// key's stored historical scopes are ignored, including unknown and person-only
// entries. Role, creator and project authority remain separate checks.
func ResolveKeyScopes(stored []string, fullAccess bool) []string {
	if !fullAccess {
		return stored
	}
	resolved := make([]string, 0, len(Registry))
	for _, permission := range Registry {
		if permission.AgentGrantable {
			resolved = append(resolved, permission.Key)
		}
	}
	return resolved
}

// KeyAllows checks the key ceiling, never the principal's role authority.
func KeyAllows(p tenant.Principal, permission string) bool {
	if p.FullAccess {
		known, ok := Lookup(permission)
		return ok && known.AgentGrantable
	}
	return containsScope(p.Scopes, permission) || CoordinatorCeiling(p.Scopes, permission)
}
