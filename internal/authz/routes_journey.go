// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for journey.
func init() {
	registerRoutes("journey", map[string]string{
		"GET /api/journey/next-actions":                  "journey.read",
		"GET /api/projects/{projectId}/journey":          "journey.read",
		"POST /api/projects/{projectId}/journey/actions": "journey.act",
		"PUT /api/projects/{projectId}/journey/profile":  "journey.act",
	})
	registerDeclarations("journey", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/journey/next-actions": true,
	})
}
