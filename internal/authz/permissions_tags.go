// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for tags. Grant locations are explicit per action.
func init() {
	registerPermissions("tags", []Permission{
		{Key: "tags.manage", Group: "Tags", Description: "Manage tags", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "tags.read", Group: "Tags", Description: "Read tags", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "tags.write", Group: "Tags", Description: "Write tags", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
