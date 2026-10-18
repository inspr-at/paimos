// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for crm. Grant locations are explicit per action.
func init() {
	registerPermissions("crm", []Permission{
		{Key: "crm.manage", Group: "CRM", Description: "Manage crm", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "crm.read", Group: "CRM", Description: "Read crm", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "crm.write", Group: "CRM", Description: "Write crm", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
