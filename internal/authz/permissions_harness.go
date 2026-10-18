// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for harness. Grant locations are explicit per action.
func init() {
	registerPermissions("harness", []Permission{
		{Key: "harness.control", Group: "Harness", Description: "Control harness", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "harness.force_stop", Group: "Harness", Description: "Force Stop harness", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "harness.manage", Group: "Harness", Description: "Manage harness", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "harness.read", Group: "Harness", Description: "Read harness", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "harness.recover", Group: "Harness", Description: "Recover harness", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "harness.watch", Group: "Harness", Description: "Watch harness", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "harness.worker", Group: "Harness", Description: "Worker harness", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "harness.write", Group: "Harness", Description: "Write harness", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
