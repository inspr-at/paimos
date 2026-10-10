// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for engine. Grant locations are explicit per action.
func init() {
	registerPermissions("engine", []Permission{
		{Key: "engine.admission", Group: "Engine", Description: "Admission engine", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "engine.manage", Group: "Engine", Description: "Manage engine", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "engine.read", Group: "Engine", Description: "Read engine", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
