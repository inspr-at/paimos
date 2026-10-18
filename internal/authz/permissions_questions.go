// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for questions. Grant locations are explicit per action.
func init() {
	registerPermissions("questions", []Permission{
		{Key: "questions.ask", Group: "Questions", Description: "Ask questions", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "questions.decide", Group: "Questions", Description: "Decide questions", Risk: "high", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
		{Key: "questions.read", Group: "Questions", Description: "Read questions", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
