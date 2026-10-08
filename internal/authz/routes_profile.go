// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for profile.
func init() {
	registerRoutes("profile", map[string]string{
		"DELETE /api/me/avatar": "profile.write|profile.portal_write",
		"GET /api/me/profile":   "profile.read|profile.portal_read",
		"PATCH /api/me/profile": "profile.write|profile.portal_write",
		"POST /api/me/avatar":   "profile.write|profile.portal_write",
	})
	registerDeclarations("profile", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"DELETE /api/me/avatar": true,
		"GET /api/me/profile":   true,
		"PATCH /api/me/profile": true,
		"POST /api/me/avatar":   true,
	})
}
