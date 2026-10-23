// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for inbox.
func init() {
	registerRoutes("inbox", map[string]string{
		"GET /api/projects/{projectId}/assistant-adapters":                        "inbox.manage",
		"POST /api/projects/{projectId}/assistant-adapters":                       "inbox.manage",
		"GET /api/projects/{projectId}/assistant-adapters/{adapterId}":            "inbox.manage",
		"POST /api/projects/{projectId}/assistant-adapters/{adapterId}/test-send": "inbox.manage",
		"GET /api/inbox/lanes":                                           "inbox.read",
		"GET /api/peers":                                                 "harness.read",
		"POST /api/peers/{sessionId}/messages":                           "inbox.send",
		"DELETE /api/inbox/targets/{targetId}":                           "inbox.send|inbox.manage",
		"GET /api/inbox/feedback-recipient":                              "inbox.send",
		"GET /api/inbox/message-status":                                  "inbox.receipt",
		"GET /api/inbox/messages":                                        "inbox.read",
		"GET /api/inbox/messages/{messageId}/receipt":                    "inbox.receipt",
		"GET /api/inbox/stream":                                          "inbox.read",
		"GET /api/inbox/targets":                                         "inbox.read",
		"GET /api/projects/{projectId}/message-deliveries":               "inbox.manage",
		"GET /api/projects/{projectId}/message-targets":                  "inbox.manage",
		"GET /api/projects/{projectId}/messages":                         "inbox.manage|harness.read",
		"GET /api/projects/{projectId}/messages/listen":                  "inbox.read",
		"GET /api/settings/inbox-delivery":                               "settings.read",
		"POST /api/inbox/messages":                                       "inbox.send",
		"POST /api/inbox/messages/{messageId}/ack":                       "inbox.send",
		"POST /api/inbox/messages/{messageId}/cancel":                    "inbox.send",
		"POST /api/inbox/targets":                                        "inbox.send|inbox.manage",
		"POST /api/projects/{projectId}/message-targets":                 "inbox.manage",
		"POST /api/projects/{projectId}/messages":                        "inbox.send",
		"POST /api/projects/{projectId}/messages/delivery-claim":         "inbox.send",
		"POST /api/projects/{projectId}/messages/delivery-complete":      "inbox.send",
		"POST /api/projects/{projectId}/messages/delivery-unavailable":   "inbox.send",
		"POST /api/projects/{projectId}/messages/{messageId}/ack":        "inbox.send",
		"POST /api/projects/{projectId}/messages/{messageId}/cancel":     "inbox.send",
		"POST /api/projects/{projectId}/messages/{messageId}/resolution": "inbox.manage",
		"PUT /api/settings/inbox-delivery":                               "settings.manage",
	})
	registerDeclarations("inbox", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/inbox/lanes":                 true,
		"GET /api/peers":                       true,
		"POST /api/peers/{sessionId}/messages": true,
	})
}
