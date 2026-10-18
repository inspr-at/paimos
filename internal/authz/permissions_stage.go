// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for stage. Grant locations are explicit per action.
func init() {
	registerPermissions("stage", []Permission{
		{Key: "stage.apply", Group: "Stage", Description: "Apply stage", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "stage.deploy", Group: "Stage", Description: "Deploy stage", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "stage.prepare", Group: "Stage", Description: "Prepare stage", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "stage.verify", Group: "Stage", Description: "Verify stage", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
