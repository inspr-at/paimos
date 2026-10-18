// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for journey. Grant locations are explicit per action.
func init() {
	registerPermissions("journey", []Permission{
		{Key: "journey.act", Group: "Journey", Description: "Act journey", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "journey.manage", Group: "Journey", Description: "Manage journey", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "journey.read", Group: "Journey", Description: "Read journey", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
