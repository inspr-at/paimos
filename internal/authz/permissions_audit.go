// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for audit. Grant locations are explicit per action.
func init() {
	registerPermissions("audit", []Permission{
		{Key: "audit.read", Group: "Audit", Description: "Read audit", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: true},
	})
}
