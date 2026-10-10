// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for events. Grant locations are explicit per action.
func init() {
	registerPermissions("events", []Permission{
		{Key: "events.read", Group: "Events", Description: "Read events", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "events.subscribe", Group: "Events", Description: "Subscribe to authorized change hints (explicit custom agent grant)", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "events.undo", Group: "Events", Description: "Undo events", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "events.undo_other", Group: "Events", Description: "Undo Other events", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
