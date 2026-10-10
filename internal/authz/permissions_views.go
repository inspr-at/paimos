// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for views. Grant locations are explicit per action.
func init() {
	registerPermissions("views", []Permission{
		{Key: "views.read", Group: "Views", Description: "Read views", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "views.share", Group: "Views", Description: "Share views", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "views.write", Group: "Views", Description: "Write views", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
