// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for requirements. Grant locations are explicit per action.
func init() {
	registerPermissions("requirements", []Permission{
		{Key: "requirements.agree", Group: "Requirements", Description: "Agree requirements", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "requirements.read", Group: "Requirements", Description: "Read requirements", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "requirements.write", Group: "Requirements", Description: "Write requirements", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
