// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for ownership. Grant locations are explicit per action.
func init() {
	registerPermissions("ownership", []Permission{
		{Key: "ownership.transfer", Group: "Ownership", Description: "Transfer workspace ownership", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
	})
}
