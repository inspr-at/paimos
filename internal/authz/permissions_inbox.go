// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for inbox. Grant locations are explicit per action.
func init() {
	registerPermissions("inbox", []Permission{
		{Key: "inbox.manage", Group: "Inbox", Description: "Manage inbox", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "inbox.read", Group: "Inbox", Description: "Read inbox", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "inbox.receipt", Group: "Inbox", Description: "Receipt inbox", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "inbox.send", Group: "Inbox", Description: "Send inbox", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
