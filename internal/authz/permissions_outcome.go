// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for outcome. Grant locations are explicit per action.
func init() {
	registerPermissions("outcome", []Permission{
		{Key: "outcome.read", Group: "Outcome", Description: "Read outcome", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "outcome.write", Group: "Outcome", Description: "Write outcome", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
