// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for imports.
func init() {
	registerRoutes("imports", map[string]string{
		"GET /api/imports":            "imports.read",
		"GET /api/imports/{importId}": "imports.read",
	})
}
