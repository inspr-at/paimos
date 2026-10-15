// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for crossreview. Grant locations are explicit per action.
func init() {
	registerPermissions("crossreview", []Permission{
		{Key: "reviews.visual_approve", Group: "Reviews", Description: "Approve visual review verdicts", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: false},
	})
}
