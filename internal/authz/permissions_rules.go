// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for rules. Grant locations are explicit per action.
func init() {
	registerPermissions("rules", []Permission{
		{Key: "rules.publish", Group: "Rules", Description: "Publish rules", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "rules.read", Group: "Rules", Description: "Read rules", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "rules.write", Group: "Rules", Description: "Write rules", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
