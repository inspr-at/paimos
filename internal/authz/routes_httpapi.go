// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for httpapi.
func init() {
	registerRoutes("httpapi", map[string]string{
		"GET /api/health":  "public",
		"GET /api/ready":   "public",
		"GET /api/version": "public",
	})
}
