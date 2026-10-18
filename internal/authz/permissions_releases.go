// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for releases. Grant locations are explicit per action.
func init() {
	registerPermissions("releases", []Permission{
		{Key: "releases.deploy", Group: "Releases", Description: "Deploy releases", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "releases.read", Group: "Releases", Description: "Read releases", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "releases.write", Group: "Releases", Description: "Write releases", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
