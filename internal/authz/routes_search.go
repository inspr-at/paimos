// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for search.
func init() {
	registerRoutes("search", map[string]string{
		"GET /api/search": "search.read",
	})
	registerDeclarations("search", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/search": true,
	})
}
