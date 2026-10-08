// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for relations. Grant locations are explicit per action.
func init() {
	registerPermissions("relations", []Permission{
		{Key: "relations.delete", Group: "Relations", Description: "Delete relations", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "relations.read", Group: "Relations", Description: "Read relations", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "relations.write", Group: "Relations", Description: "Write relations", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
