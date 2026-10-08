// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for auth.
func init() {
	registerRoutes("auth", map[string]string{
		"DELETE /api/agent-keys/{id}":                        "keys.manage",
		"GET /api/agent-keys":                                "keys.read",
		"GET /api/agent-keys/{id}/scopes":                    "keys.manage",
		"GET /api/agentd/step-ups/{challenge_id}":            "harness.worker",
		"GET /api/agents/plan":                               "agents.plan.read",
		"GET /api/auth/callback":                             "public",
		"GET /api/auth/login":                                "public",
		"GET /api/key-trim-proposals":                        "keys.manage",
		"GET /api/me":                                        AuthenticatedRoute,
		"GET /api/people/{principalId}/avatar/{size}":        "profile.read",
		"PATCH /api/agent-keys/{id}/scopes":                  "keys.manage",
		"POST /api/agent-keys":                               "keys.manage",
		"POST /api/agent-keys/{id}/adopt":                    "keys.manage",
		"POST /api/agent-keys/{id}/trim-proposals":           "approvals.request",
		"POST /api/auth/dev-login":                           "public",
		"POST /api/auth/logout":                              "public",
		"POST /api/key-trim-proposals/{proposalId}/decision": "keys.manage",
		"POST /api/key-trim-proposals/{proposalId}/restore":  "keys.manage",
		"PUT /api/agent-keys/{id}/owner-workstation":         "keys.manage",
	})
	registerDeclarations("auth", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/me": true,
		"GET /api/people/{principalId}/avatar/{size}": true,
	})
}
