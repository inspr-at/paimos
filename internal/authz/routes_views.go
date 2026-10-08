// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for views.
func init() {
	registerRoutes("views", map[string]string{
		"DELETE /api/views/{viewId}":       "views.write",
		"GET /api/preferences/{key}":       "views.read",
		"GET /api/views":                   "views.read",
		"GET /api/views/{viewId}":          "views.read",
		"PATCH /api/views/{viewId}":        "views.write",
		"POST /api/views":                  "views.write",
		"POST /api/views/{viewId}/restore": "views.write",
		"PUT /api/preferences/{key}":       "views.write",
	})
	registerDeclarations("views", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/preferences/{key}": true,
		"GET /api/views":             true,
		"GET /api/views/{viewId}":    true,
		"PUT /api/preferences/{key}": true,
	})
}
