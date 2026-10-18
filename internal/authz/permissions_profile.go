// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for profile. Grant locations are explicit per action.
func init() {
	registerPermissions("profile", []Permission{
		{Key: "profile.manage", Group: "Profile", Description: "Manage profile", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "profile.portal_read", Group: "Profile", Description: "Portal Read profile", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
		{Key: "profile.portal_write", Group: "Profile", Description: "Portal Write profile", Risk: "medium", GrantableAt: []string{"workspace"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
		{Key: "profile.read", Group: "Profile", Description: "Read profile", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "profile.write", Group: "Profile", Description: "Write profile", Risk: "medium", GrantableAt: []string{"workspace"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
