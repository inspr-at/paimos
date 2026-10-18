// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for members. Grant locations are explicit per action.
func init() {
	registerPermissions("members", []Permission{
		{Key: "members.manage", Group: "Members", Description: "Manage members", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "members.read", Group: "Members", Description: "Read members", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
