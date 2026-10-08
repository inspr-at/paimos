// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for quotes. Grant locations are explicit per action.
func init() {
	registerPermissions("quotes", []Permission{
		{Key: "quotes.accept", Group: "Quotes", Description: "Accept quotes", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "quotes.delete", Group: "Quotes", Description: "Delete quotes", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "quotes.issue", Group: "Quotes", Description: "Issue quotes", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "quotes.manage", Group: "Quotes", Description: "Manage quotes", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "quotes.portal_accept", Group: "Quotes", Description: "Portal Accept quotes", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
		{Key: "quotes.portal_read", Group: "Quotes", Description: "Portal Read quotes", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
		{Key: "quotes.read", Group: "Quotes", Description: "Read quotes", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "quotes.write", Group: "Quotes", Description: "Write quotes", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
