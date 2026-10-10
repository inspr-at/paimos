// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for plugins.
func init() {
	registerRoutes("plugins", map[string]string{
		"GET /api/plugins":                         "plugins.read",
		"PUT /api/plugins/{pluginId}/installation": "plugins.manage",
	})
}
