// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for authz. Grant locations are explicit per action.
func init() {
	registerPermissions("authz", []Permission{
		{Key: "authz.read", Group: "Access", Description: "Read authz", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
