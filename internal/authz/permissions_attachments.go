// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for attachments. Grant locations are explicit per action.
func init() {
	registerPermissions("attachments", []Permission{
		{Key: "attachments.delete", Group: "Attachments", Description: "Delete attachments", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "attachments.read", Group: "Attachments", Description: "Read attachments", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "attachments.write", Group: "Attachments", Description: "Write attachments", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
