// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for delivery. Grant locations are explicit per action.
func init() {
	registerPermissions("delivery", []Permission{
		{Key: "delivery.manage", Group: "Delivery", Description: "Manage delivery", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery.read", Group: "Delivery", Description: "Read delivery", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery.route", Group: "Delivery", Description: "Route delivery", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
