// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for projectgroups.
func init() {
	registerRoutes("projectgroups", map[string]string{
		"DELETE /api/project-groups/{groupId}": "project_groups.write",
		"GET /api/project-groups":              "project_groups.read",
		"PATCH /api/project-groups/{groupId}":  "project_groups.write",
		"POST /api/project-groups":             "project_groups.write",
		"POST /api/project-groups/assign":      "project_groups.write",
	})
}
