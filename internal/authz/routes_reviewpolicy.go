// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for reviewpolicy.
func init() {
	registerRoutes("reviewpolicy", map[string]string{
		"DELETE /api/projects/{projectId}/review-policy": "reviewpolicy.manage",
		"GET /api/projects/{projectId}/review-policy":    "reviewpolicy.read",
		"GET /api/settings/review-policy":                "reviewpolicy.read",
		"PUT /api/projects/{projectId}/review-policy":    "reviewpolicy.manage",
		"PUT /api/settings/review-policy":                "reviewpolicy.manage",
	})
}
