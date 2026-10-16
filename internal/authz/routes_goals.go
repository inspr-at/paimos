// SPDX-License-Identifier: AGPL-3.0-only
package authz

func init() {
	registerRoutes("goals", map[string]string{
		"GET /api/goals":                  "nodes.read",
		"GET /api/goals/{goalId}":         "nodes.read",
		"GET /api/goals/{goalId}/history": "nodes.read",
		"POST /api/goals":                 "nodes.write",
		"PATCH /api/goals/{goalId}":       "nodes.write",
	})
	registerDeclarations("goals", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/goals":                  true,
		"GET /api/goals/{goalId}":         true,
		"GET /api/goals/{goalId}/history": true,
	})
	registerDeclarations("goals", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/goals":           true,
		"PATCH /api/goals/{goalId}": true,
	})
}
