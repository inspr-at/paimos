// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for events.
func init() {
	registerRoutes("events", map[string]string{
		"GET /api/events":                        "events.read",
		"GET /api/events/activity":               "events.read",
		"GET /api/events/stream":                 "events.read",
		"GET /api/events/subscribe":              "events.subscribe",
		"GET /api/events/{eventId}/undo-preview": "events.undo",
		"POST /api/events/{eventId}/undo":        "events.undo",
	})
	registerDeclarations("events", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/events":          true,
		"GET /api/events/activity": true,
		"GET /api/events/stream":   true,
	})
}
