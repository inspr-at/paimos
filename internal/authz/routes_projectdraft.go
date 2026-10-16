// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route declarations for projectdraft (AEON-1441). The handoff from START is a
// public capability route; creating a project from it needs nodes.write, and
// every node, knowledge and attachment guard still runs inside the write.
func init() {
	registerRoutes("projectdraft", map[string]string{
		"GET /api/import/project-draft":         "nodes.write",
		"POST /api/import/project-draft/create": "nodes.write",
		"POST /import/project-draft":            PublicRoute,
	})
}
