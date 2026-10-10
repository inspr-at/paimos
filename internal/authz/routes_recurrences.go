// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for recurrences. Target route scope is the
// output project; handlers additionally check the definition scope and owner.
// Personal definitions require the canonical person, workspace definitions a
// workspace grant, and project definitions a grant in their scope project.
func init() {
	registerRoutes("recurrences", map[string]string{
		"GET /api/recurrences/{recurrenceId}/guardrails": "nodes.read|recurrences.manage",
		"PUT /api/recurrences/{recurrenceId}/guardrails": "recurrences.manage",
		"DELETE /api/recurrences/{recurrenceId}":         "recurrences.manage",
		// Read-only people enter with nodes.read. Recurrence handlers still require
		// recurrences.manage for agents, including their explicit role and key scope.
		"GET /api/recurrences":                                  "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}":                   "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}/history":           "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}/preview":           "nodes.read|recurrences.manage",
		"GET /api/recurrences/{recurrenceId}/releases":          "nodes.read|recurrences.manage",
		"POST /api/recurrences":                                 "recurrences.manage",
		"POST /api/recurrences/preview":                         "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/pause":            "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/resume":           "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/run-now":          "recurrences.manage",
		"POST /api/recurrences/{recurrenceId}/events":           "recurrences.manage",
		"PUT /api/recurrences/{recurrenceId}":                   "recurrences.manage",
		"PUT /api/recurrences/{recurrenceId}/execution-consent": "recurrences.manage",
	})
	registerDeclarations("recurrences", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/recurrences": true,
	})
	registerDeclarations("recurrences", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/recurrences":         true,
		"POST /api/recurrences/preview": true,
	})
}
