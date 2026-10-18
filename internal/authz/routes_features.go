// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for features.
func init() {
	registerRoutes("features", map[string]string{
		"GET /api/features":                "nodes.read",
		"GET /api/settings/features":       "settings.manage",
		"PUT /api/settings/features/{key}": "settings.manage",
	})
	registerDeclarations("features", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/features": true,
	})
}
