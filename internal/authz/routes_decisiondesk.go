// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for decisiondesk.
func init() {
	registerRoutes("decisiondesk", map[string]string{
		"GET /api/decision-desk/projection": "profile.read",
	})
}
