// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for imports. Grant locations are explicit per action.
func init() {
	registerPermissions("imports", []Permission{
		{Key: "imports.manage", Group: "Imports", Description: "Manage imports", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "imports.read", Group: "Imports", Description: "Read imports", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
