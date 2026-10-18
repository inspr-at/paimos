// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for work_orders. Grant locations are explicit per action.
func init() {
	registerPermissions("work_orders", []Permission{
		{Key: "work_orders.assign", Group: "Work Orders", Description: "Assign work orders", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "work_orders.read", Group: "Work Orders", Description: "Read work orders", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "work_orders.write", Group: "Work Orders", Description: "Write work orders", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
