// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for intake. Grant locations are explicit per action.
func init() {
	registerPermissions("intake", []Permission{
		{Key: "intake.decide", Group: "Intake", Description: "Decide intake", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "intake.read", Group: "Intake", Description: "Read intake", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "intake.write", Group: "Intake", Description: "Write intake", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
