// SPDX-License-Identifier: AGPL-3.0-only

package authz

func init() {
	registerRoutes("assistants", map[string]string{
		"GET /api/assistants":                        "members.read",
		"GET /api/assistants/{principalId}":          "members.read",
		"POST /api/assistants":                       "agents.create",
		"PATCH /api/assistants/{principalId}":        "agents.create",
		"POST /api/assistants/{principalId}/disable": "agents.create",
		"GET /api/assistants/{principalId}/policy":   "members.read|inbox.read",
		"PUT /api/assistants/{principalId}/policy":   "agents.create",
	})
}

// AssistantPolicyReadRoute is the one new self-policy read, explicitly
// authorized by AEON-1701. The handler checks self identity and live inbox.read.
// Existing messaging route permissions and the provisioning exception stay put.
func AssistantPolicyReadRoute(pattern string) bool {
	return pattern == "GET /api/assistants/{principalId}/policy"
}
