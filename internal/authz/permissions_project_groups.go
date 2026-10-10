// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for project_groups. Grant locations are explicit per action.
func init() {
	registerPermissions("project_groups", []Permission{
		{Key: "project_groups.read", Group: "Project Groups", Description: "Read project groups", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "project_groups.write", Group: "Project Groups", Description: "Write project groups", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
