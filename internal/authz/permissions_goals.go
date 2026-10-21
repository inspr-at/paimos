// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for goals. Grant locations are explicit per action.
// These are the agent-key ceiling only. Goal routes stay on nodes.read and
// nodes.write, and the handlers recheck those inside the write transaction.
func init() {
	registerPermissions("goals", []Permission{
		{Key: "goals.read", Group: "Goals", Description: "Read goals", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "goals.write", Group: "Goals", Description: "Write goals", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
