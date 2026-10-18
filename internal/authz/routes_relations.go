// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for relations.
func init() {
	registerRoutes("relations", map[string]string{
		"DELETE /api/relations/{relationId}": "relations.delete",
		"GET /api/relations":                 "relations.read",
		"POST /api/relations":                "relations.write",
	})
	registerDeclarations("relations", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/relations": true,
	})
	registerDeclarations("relations", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/relations": true,
	})
}
