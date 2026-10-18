// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for kinds. Grant locations are explicit per action.
func init() {
	registerPermissions("kinds", []Permission{
		{Key: "kinds.manage", Group: "Kinds", Description: "Manage kinds", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "kinds.read", Group: "Kinds", Description: "Read kinds", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
