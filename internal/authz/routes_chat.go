// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for chat.
func init() {
	registerRoutes("chat", map[string]string{
		"GET /api/chat-threads/{id}":                          "chat.read",
		"GET /api/chat-threads/{id}/messages":                 "chat.read",
		"GET /api/chat-threads/{id}/read-marker":              "chat.read",
		"POST /api/chat-deliveries/binding/resolve":           "chat.receive",
		"POST /api/chat-threads/{id}/binding":                 "chat.bind",
		"POST /api/projects/{projectId}/chat-roles":           "chat.bind",
		"POST /api/projects/{projectId}/chat-threads/resolve": "chat.read",
		"PUT /api/chat-threads/{id}/read-marker":              "chat.read",
	})
	registerDeclarations("chat", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/chat-threads/{id}": true,
	})
	registerDeclarations("chat", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/chat-deliveries/binding/resolve": true,
		"POST /api/chat-threads/{id}/binding":       true,
	})
}
