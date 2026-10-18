// SPDX-License-Identifier: AGPL-3.0-only

package authz

// C0 declares existing outer permissions only. Enabled implementations must
// recheck target visibility, current actor grants and mode-specific authority.
func init() {
	registerRoutes("native-sessions", map[string]string{
		"POST /api/projects/{projectId}/native-sessions":                                                  "harness.control",
		"GET /api/projects/{projectId}/native-sessions/{ownedSessionId}":                                  "harness.read",
		"GET /api/projects/{projectId}/harness-sessions/{sessionId}/native-context":                       "harness.read",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/resume":                          "harness.control",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/input":                           "chat.send|inbox.send",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/interrupt":                       "harness.control",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/stop":                            "harness.control",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach":                          "harness.read",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach/renew":                    "harness.read",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/detach":                          "harness.read",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/take":                            "harness.control",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/return":                          "harness.control",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/approvals/{approvalId}/decision": "questions.decide",
		"GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/stream":                           "harness.read",
		"GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/receipts":                         "harness.read",
		"GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/events":                           "harness.read",
		"POST /api/agentd/native-sessions/claim":                                                          "harness.worker",
		"POST /api/agentd/native-sessions/receipts":                                                       "harness.worker",
		"POST /api/agentd/native-sessions/events":                                                         "harness.worker",
		"POST /api/agentd/native-sessions/finals":                                                         "harness.worker",
		"GET /api/agentd/native-sessions/commands":                                                        "harness.worker",
		"POST /api/sessions/{id}/control/end":                                                             "harness.control",
	})
}
