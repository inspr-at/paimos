// SPDX-License-Identifier: AGPL-3.0-only
package authz

func init() {
	registerRoutes("goals", map[string]string{
		"GET /api/goals":                  "nodes.read",
		"GET /api/goals/epics":            "nodes.read",
		"GET /api/goals/{goalId}":         "nodes.read",
		"GET /api/goals/{goalId}/history": "nodes.read",
		"GET /api/goals/{goalId}/series":  "nodes.read",
		"GET /api/goals/status-note":      "nodes.read",
		"POST /api/goals":                 "nodes.write",
		"PATCH /api/goals/{goalId}":       "nodes.write",
		"PATCH /api/goals/status-note":    "nodes.write",
		// AEON-1614: Hide for me. People only; the handler refuses agents and API keys.
		"PUT /api/me/goals/{goalId}/hidden":    "nodes.read",
		"DELETE /api/me/goals/{goalId}/hidden": "nodes.read",
	})
	registerDeclarations("goals", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/goals":                  true,
		"GET /api/goals/epics":            true,
		"GET /api/goals/{goalId}":         true,
		"GET /api/goals/{goalId}/history": true,
		"GET /api/goals/{goalId}/series":  true,
		// The caller's own preference; the handler requires nodes.read in the goal's project inside the write.
		"PUT /api/me/goals/{goalId}/hidden":    true,
		"DELETE /api/me/goals/{goalId}/hidden": true,
	})
	registerDeclarations("goals", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/goals":           true,
		"PATCH /api/goals/{goalId}": true,
	})
}
