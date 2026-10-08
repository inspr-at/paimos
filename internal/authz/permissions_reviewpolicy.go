// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for reviewpolicy. Grant locations are explicit per action.
func init() {
	registerPermissions("reviewpolicy", []Permission{
		{Key: "reviewpolicy.manage", Group: "Reviewpolicy", Description: "Manage reviewpolicy", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "reviewpolicy.read", Group: "Reviewpolicy", Description: "Read reviewpolicy", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
