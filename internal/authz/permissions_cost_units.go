// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for cost_units. Grant locations are explicit per action.
func init() {
	registerPermissions("cost_units", []Permission{
		{Key: "cost_units.manage", Group: "Cost Units", Description: "Manage cost units", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "cost_units.read", Group: "Cost Units", Description: "Read cost units", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "cost_units.write", Group: "Cost Units", Description: "Write cost units", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
