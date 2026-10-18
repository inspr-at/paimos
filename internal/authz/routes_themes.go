// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for themes.
func init() {
	registerRoutes("themes", map[string]string{
		"DELETE /api/themes/{themeId}": "profile.write|profile.portal_write|settings.manage",
		"GET /api/me/theme":            "profile.read|profile.portal_read",
		// Theme handlers decide person ownership or workspace settings authority
		// again inside the final fenced mutation transaction.
		"GET /api/themes":                      "profile.read|profile.portal_read",
		"GET /api/themes/{themeId}":            "profile.read|profile.portal_read",
		"PATCH /api/themes/{themeId}":          "profile.write|profile.portal_write|settings.manage",
		"POST /api/themes":                     "profile.write|profile.portal_write|settings.manage",
		"POST /api/themes/{themeId}/duplicate": "profile.write|profile.portal_write|settings.manage",
		"PUT /api/me/theme":                    "profile.write|profile.portal_write",
	})
	registerDeclarations("themes", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"DELETE /api/themes/{themeId}":         true,
		"GET /api/me/theme":                    true,
		"GET /api/themes":                      true,
		"GET /api/themes/{themeId}":            true,
		"PATCH /api/themes/{themeId}":          true,
		"POST /api/themes":                     true,
		"POST /api/themes/{themeId}/duplicate": true,
		"PUT /api/me/theme":                    true,
	})
}
