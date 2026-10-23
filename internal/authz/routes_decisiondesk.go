// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for decisiondesk.
func init() {
	registerRoutes("decisiondesk", map[string]string{
		"GET /api/decision-desk/instances":           "profile.read|settings.manage",
		"GET /api/decision-desk/owner-summary":       "public",
		"OPTIONS /api/decision-desk/owner-summary":   "public",
		"GET /api/decision-desk/connections":         "profile.read|settings.manage",
		"POST /api/decision-desk/connections":        "profile.read|settings.manage",
		"DELETE /api/decision-desk/connections/{id}": "profile.read|settings.manage",
		"GET /api/decision-desk/projection":          "profile.read|settings.manage",
	})
}
