// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for intake.
func init() {
	registerRoutes("intake", map[string]string{
		"GET /api/projects/{projectId}/intake":                           "intake.read",
		"POST /api/projects/{projectId}/intake/drafts":                   "intake.write",
		"POST /api/projects/{projectId}/intake/drafts/{draftId}/accept":  "intake.decide",
		"POST /api/projects/{projectId}/intake/drafts/{draftId}/replace": "intake.write",
		"POST /api/projects/{projectId}/intake/sources":                  "intake.write",
		"POST /api/projects/{projectId}/intake/transcript-turns":         "intake.write",
	})
}
