// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for account. Grant locations are explicit per action.
func init() {
	registerPermissions("account", []Permission{
		{Key: "account.manage", Group: "Account", Description: "Manage account", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "account.overview.read", Group: "Account", Description: "Read all enrolled account capacity values", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "account.probe", Group: "Account", Description: "Probe account", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "account.read", Group: "Account", Description: "Read account", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "account.route", Group: "Account", Description: "Route account", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
