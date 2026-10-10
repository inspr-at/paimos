// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for settings. Grant locations are explicit per action.
func init() {
	registerPermissions("settings", []Permission{
		{Key: "settings.manage", Group: "Settings", Description: "Manage settings", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "settings.read", Group: "Settings", Description: "Read settings", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
