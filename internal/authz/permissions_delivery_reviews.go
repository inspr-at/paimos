// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for delivery_reviews. Grant locations are explicit per action.
func init() {
	registerPermissions("delivery_reviews", []Permission{
		{Key: "delivery_reviews.claim", Group: "Delivery Reviews", Description: "Claim delivery reviews", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_reviews.manage", Group: "Delivery Reviews", Description: "Manage delivery reviews", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_reviews.read", Group: "Delivery Reviews", Description: "Read delivery reviews", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_reviews.report", Group: "Delivery Reviews", Description: "Report delivery reviews", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
