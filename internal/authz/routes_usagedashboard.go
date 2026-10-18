// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for usagedashboard.
func init() {
	registerRoutes("usagedashboard", map[string]string{
		"GET /api/projects/{projectId}/lead/usage": "harness.read",
		"GET /api/usage/dashboard":                 "harness.read",
		"GET /api/usage/model-estimates":           "harness.read",
	})
	registerDeclarations("usagedashboard", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/usage/dashboard":       true,
		"GET /api/usage/model-estimates": true,
	})
}
