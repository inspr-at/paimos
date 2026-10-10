// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for roles. Grant locations are explicit per action.
func init() {
	registerPermissions("roles", []Permission{
		{Key: "roles.manage", Group: "Roles", Description: "Manage roles", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "roles.read", Group: "Roles", Description: "Read roles", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
