// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for nodes. Grant locations are explicit per action.
func init() {
	registerPermissions("nodes", []Permission{
		{Key: "nodes.configure", Group: "Nodes", Description: "Configure nodes", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "nodes.delete", Group: "Nodes", Description: "Delete nodes", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "nodes.move", Group: "Nodes", Description: "Move nodes", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "nodes.read", Group: "Nodes", Description: "Read nodes", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "nodes.restore", Group: "Nodes", Description: "Restore nodes", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "nodes.write", Group: "Nodes", Description: "Write nodes", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
