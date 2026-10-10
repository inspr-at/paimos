// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Permission declarations for chat. Grant locations are explicit per action.
func init() {
	registerPermissions("chat", []Permission{
		{Key: "chat.bind", Group: "Chat", Description: "Bind chat", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: false, OwnerWorkstationGrantable: false},
		{Key: "chat.read", Group: "Chat", Description: "Read chat", Risk: "low", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "chat.receive", Group: "Chat", Description: "Receive chat", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
		{Key: "chat.send", Group: "Chat", Description: "Send chat", Risk: "medium", GrantableAt: []string{"workspace", "project"}, AgentGrantable: true, OwnerWorkstationGrantable: true},
	})
}
