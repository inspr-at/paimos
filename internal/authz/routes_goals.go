// SPDX-License-Identifier: AGPL-3.0-only
package authz

func init() {
	registerRoutes("goals", map[string]string{
		"POST /api/goals":           "nodes.write",
		"PATCH /api/goals/{goalId}": "nodes.write",
	})
	registerDeclarations("goals", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/goals":           true,
		"PATCH /api/goals/{goalId}": true,
	})
}
