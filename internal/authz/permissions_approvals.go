// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for approvals. Grant locations are explicit per action.
func init() {
	registerPermissions("approvals", []Permission{
		{Key: "approvals.decide", Group: "Approvals", Description: "Decide approvals", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "approvals.decide_high", Group: "Approvals", Description: "Decide High approvals", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
		{Key: "approvals.propose", Group: "Approvals", Description: "Propose approvals", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "approvals.read", Group: "Approvals", Description: "Read approvals", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "approvals.request", Group: "Approvals", Description: "Request approvals", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "approvals.revoke", Group: "Approvals", Description: "Revoke approvals", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
