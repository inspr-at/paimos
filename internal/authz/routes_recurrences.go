// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for recurrences.
func init() {
	registerRoutes("recurrences", map[string]string{
		"DELETE /api/recurrences/{recurrenceId}": "recurrences.manage",
		// Read-only people enter with nodes.read. Recurrence handlers still require
		// recurrences.manage for agents, including their explicit role and key scope.
		"GET /api/recurrences":                         "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}":          "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}/history":  "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}/preview":  "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}/releases": "nodes.read|recurrences.manage",
		"POST /api/recurrences":                        "recurrences.manage",
		"POST /api/recurrences/preview":                "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/pause":   "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/resume":  "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/run-now": "recurrences.manage",
		"PUT /api/recurrences/{recurrenceId}":          "recurrences.manage",
	})
	registerDeclarations("recurrences", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/recurrences": true,
	})
	registerDeclarations("recurrences", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/recurrences":         true,
		"POST /api/recurrences/preview": true,
	})
}
