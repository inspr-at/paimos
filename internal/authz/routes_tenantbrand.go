// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for tenantbrand.
func init() {
	registerRoutes("tenantbrand", map[string]string{
		"DELETE /api/settings/brand/logo/{variant}": "settings.manage",
		"GET /api/brand/logo/{variant}":             "profile.read|profile.portal_read",
		"GET /api/settings/brand":                   "settings.manage",
		"PUT /api/settings/brand":                   "settings.manage",
		"PUT /api/settings/brand/logo/{variant}":    "settings.manage",
	})
	registerDeclarations("tenantbrand", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/brand/logo/{variant}": true,
	})
}
