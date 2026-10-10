// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/directory.
func init() {
	registerRoutes("business_directory", map[string]string{
		"GET /api/business/principals": "members.read",
	})
}
