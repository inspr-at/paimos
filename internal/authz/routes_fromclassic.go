// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for fromclassic.
func init() {
	registerRoutes("fromclassic", map[string]string{
		"GET /api/from-classic": "nodes.read",
	})
	registerDeclarations("fromclassic", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/from-classic": true,
	})
}
