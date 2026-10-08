// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for delivery_queue. Grant locations are explicit per action.
func init() {
	registerPermissions("delivery_queue", []Permission{
		{Key: "delivery_queue.claim", Group: "Delivery Queue", Description: "Claim delivery queue", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_queue.manage", Group: "Delivery Queue", Description: "Manage delivery queue", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "delivery_queue.read", Group: "Delivery Queue", Description: "Read delivery queue", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
