// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for model_prefs. Grant locations are explicit per action.
func init() {
	registerPermissions("model_prefs", []Permission{
		{Key: "model_prefs.manage", Group: "Model Prefs", Description: "Manage model prefs", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
	})
}
