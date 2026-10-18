// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for releasehistory.
func init() {
	registerRoutes("releasehistory", map[string]string{
		"DELETE /api/releases/{version}/presentation": "releases.deploy",
		"GET /api/releases":                           "releases.read",
		"GET /api/releases/pending":                   "releases.read",
		"GET /api/releases/{version}":                 "releases.read",
		"PUT /api/releases/{version}/presentation":    "releases.deploy",
	})
	registerDeclarations("releasehistory", "public_product", publicProductRoutes, map[string]bool{
		"GET /api/releases":           true,
		"GET /api/releases/pending":   true,
		"GET /api/releases/{version}": true,
	})
}
