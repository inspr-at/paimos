// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for models. Grant locations are explicit per action.
func init() {
	registerPermissions("models", []Permission{
		{Key: "models.manage", Group: "Models", Description: "Manage models", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "models.read", Group: "Models", Description: "Read models", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "models.refresh", Group: "Models", Description: "Refresh models", Risk: "medium", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "models.report", Group: "Models", Description: "Report models", Risk: "medium", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "models.resolve", Group: "Models", Description: "Resolve models", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
