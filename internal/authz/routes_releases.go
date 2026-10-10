// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for releases.
func init() {
	registerRoutes("releases", map[string]string{
		"GET /api/projects/{projectId}/release-memberships":                 "releases.read",
		"GET /api/projects/{projectId}/releases":                            "releases.read",
		"GET /api/projects/{projectId}/releases/{releaseId}/note-snapshot":  "releases.read",
		"GET /api/projects/{projectId}/releases/{releaseId}/ticket-options": "releases.read",
		"GET /api/projects/{projectId}/releases/{releaseId}/walker":         "releases.read",
		"POST /api/projects/{projectId}/releases":                           "releases.write",
		"POST /api/projects/{projectId}/releases/{releaseId}/membership":    "releases.write",
		"POST /api/projects/{projectId}/releases/{releaseId}/tickets":       "releases.write",
		"PUT /api/projects/{projectId}/releases/{releaseId}/plan":           "releases.write",
	})
}
