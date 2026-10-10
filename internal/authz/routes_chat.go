// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for chat.
func init() {
	registerRoutes("chat", map[string]string{
		"GET /api/chat-threads/{id}":                          "chat.read",
		"GET /api/chat-threads/{id}/messages":                 "chat.read",
		"GET /api/chat-threads/{id}/read-marker":              "chat.read",
		"GET /api/chat-threads/{id}/live":                     "chat.read",
		"GET /api/chat-threads/{id}/outbox":                   "chat.read",
		"POST /api/chat-threads/{id}/outbox":                  "chat.send",
		"POST /api/chat-deliveries/live":                      "chat.receive",
		"POST /api/chat-deliveries/outbox":                    "chat.receive",
		"POST /api/chat-deliveries/outbox/receipt":            "chat.receive",
		"POST /api/chat-deliveries/final":                     "chat.send",
		"POST /api/chat-deliveries/binding/resolve":           "chat.receive",
		"POST /api/chat-threads/{id}/binding":                 "chat.bind",
		"POST /api/projects/{projectId}/chat-roles":           "chat.bind",
		"POST /api/projects/{projectId}/chat-threads/resolve": "chat.read",
		"PUT /api/chat-threads/{id}/read-marker":              "chat.read",
	})
	registerDeclarations("chat", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/chat-threads/{id}":        true,
		"GET /api/chat-threads/{id}/live":   true,
		"GET /api/chat-threads/{id}/outbox": true,
	})
	registerDeclarations("chat", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/chat-deliveries/binding/resolve": true,
		"POST /api/chat-threads/{id}/binding":       true,
		"POST /api/chat-threads/{id}/outbox":        true,
		"POST /api/chat-deliveries/live":            true,
		"POST /api/chat-deliveries/outbox":          true,
		"POST /api/chat-deliveries/outbox/receipt":  true,
		"POST /api/chat-deliveries/final":           true,
	})
}
