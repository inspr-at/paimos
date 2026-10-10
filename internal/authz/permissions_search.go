// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for search. Grant locations are explicit per action.
func init() {
	registerPermissions("search", []Permission{
		{Key: "search.read", Group: "Search", Description: "Read search", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
