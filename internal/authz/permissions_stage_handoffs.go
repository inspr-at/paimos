// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for stage_handoffs. Grant locations are explicit per action.
func init() {
	registerPermissions("stage_handoffs", []Permission{
		{Key: "stage_handoffs.decide", Group: "Stage Handoffs", Description: "Decide stage handoffs", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "stage_handoffs.read", Group: "Stage Handoffs", Description: "Read stage handoffs", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "stage_handoffs.write", Group: "Stage Handoffs", Description: "Write stage handoffs", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
