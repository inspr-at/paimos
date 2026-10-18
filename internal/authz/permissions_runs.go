// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for runs. Grant locations are explicit per action.
func init() {
	registerPermissions("runs", []Permission{
		{Key: "runs.claim", Group: "Runs", Description: "Claim runs", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "runs.control", Group: "Runs", Description: "Control runs", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "runs.read", Group: "Runs", Description: "Read runs", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "runs.write", Group: "Runs", Description: "Write runs", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
