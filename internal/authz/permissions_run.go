// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for run. Grant locations are explicit per action.
func init() {
	registerPermissions("run", []Permission{
		{Key: "run.claim", Group: "Run", Description: "Claim run", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "run.create", Group: "Run", Description: "Create run", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "run.read", Group: "Run", Description: "Read run", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "run.telemetry", Group: "Run", Description: "Telemetry run", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
