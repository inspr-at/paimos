// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for greetings.
func init() {
	registerRoutes("greetings", map[string]string{
		"GET /api/me/greeting": "profile.read|profile.portal_read",
	})
	registerDeclarations("greetings", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/me/greeting": true,
	})
}
