// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for delivery_ship. Grant locations are explicit per action.
func init() {
	registerPermissions("delivery_ship", []Permission{
		{Key: "delivery_ship.claim", Group: "Delivery Ship", Description: "Claim delivery ship", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_ship.manage", Group: "Delivery Ship", Description: "Manage delivery ship", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_ship.read", Group: "Delivery Ship", Description: "Read delivery ship", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
