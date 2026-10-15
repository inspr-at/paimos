// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for agents. Grant locations are explicit per action.
func init() {
	registerPermissions("agents", []Permission{
		{Key: "agents.create", Group: "Agents", Description: "Provision messaging assistants", Risk: "high", GrantableAt: []string{"workspace"}},
		{Key: "agents.plan.read", Group: "Agents", Description: "Read the person's agent start plan and running counts", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
