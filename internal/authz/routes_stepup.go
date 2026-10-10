// SPDX-License-Identifier: AGPL-3.0-only
package authz

// Native handlers filter agent ownership and recheck the target permission.
func init() {
	registerRoutes("stepup", map[string]string{
		"POST /api/stepup-requests":                      "approvals.request",
		"GET /api/stepup-requests":                       "profile.read|settings.manage|approvals.request",
		"GET /api/stepup-requests/{requestId}":           "profile.read|settings.manage|approvals.request",
		"POST /api/stepup-requests/{requestId}/options":  "profile.read|settings.manage",
		"POST /api/stepup-requests/{requestId}/approve":  "profile.read|settings.manage",
		"POST /api/stepup-requests/{requestId}/decline":  "profile.read|settings.manage",
		"POST /api/stepup-requests/{requestId}/withdraw": "approvals.request",
	})
	registerDeclarations("stepup", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"POST /api/stepup-requests":                      true,
		"GET /api/stepup-requests":                       true,
		"GET /api/stepup-requests/{requestId}":           true,
		"POST /api/stepup-requests/{requestId}/options":  true,
		"POST /api/stepup-requests/{requestId}/approve":  true,
		"POST /api/stepup-requests/{requestId}/decline":  true,
		"POST /api/stepup-requests/{requestId}/withdraw": true,
	})
}
