// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for keys. Grant locations are explicit per action.
func init() {
	registerPermissions("keys", []Permission{
		{Key: "keys.manage", Group: "API keys", Description: "Manage keys", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "keys.read", Group: "API keys", Description: "Read keys", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
	})
}
