// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for plugins. Grant locations are explicit per action.
func init() {
	registerPermissions("plugins", []Permission{
		{Key: "plugins.invoke", Group: "Plugins", Description: "Invoke plugins", Risk: "medium", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "plugins.manage", Group: "Plugins", Description: "Manage plugins", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "plugins.read", Group: "Plugins", Description: "Read plugins", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
