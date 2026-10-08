// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for recurrences. Grant locations are explicit per action.
func init() {
	registerPermissions("recurrences", []Permission{
		{Key: "recurrences.manage", Group: "Recurrences", Description: "Manage recurrences", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
