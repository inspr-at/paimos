// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for comments. Grant locations are explicit per action.
func init() {
	registerPermissions("comments", []Permission{
		{Key: "comments.delete", Group: "Comments", Description: "Delete comments", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "comments.read", Group: "Comments", Description: "Read comments", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "comments.write", Group: "Comments", Description: "Write comments", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
