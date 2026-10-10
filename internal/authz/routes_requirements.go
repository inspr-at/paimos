// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for requirements.
func init() {
	registerRoutes("requirements", map[string]string{
		"GET /api/projects/{projectId}/requirements":        "requirements.read",
		"POST /api/projects/{projectId}/requirements":       "requirements.write",
		"POST /api/projects/{projectId}/requirements/agree": "requirements.agree",
	})
}
