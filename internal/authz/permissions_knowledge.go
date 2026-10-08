// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for knowledge. Grant locations are explicit per action.
func init() {
	registerPermissions("knowledge", []Permission{
		{Key: "knowledge.delete", Group: "Knowledge", Description: "Delete knowledge", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "knowledge.read", Group: "Knowledge", Description: "Read knowledge", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "knowledge.write", Group: "Knowledge", Description: "Write knowledge", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
