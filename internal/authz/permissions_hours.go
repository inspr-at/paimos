// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for hours. Grant locations are explicit per action.
func init() {
	registerPermissions("hours", []Permission{
		{Key: "hours.approve", Group: "Hours", Description: "Approve hours", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "hours.read", Group: "Hours", Description: "Read hours", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "hours.write", Group: "Hours", Description: "Write hours", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
