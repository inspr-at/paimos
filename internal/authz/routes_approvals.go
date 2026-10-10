// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for approvals.
func init() {
	registerRoutes("approvals", map[string]string{
		"GET /api/approvals":                        "approvals.read",
		"GET /api/approvals/{approvalId}":           "approvals.read",
		"POST /api/approvals":                       "approvals.request",
		"POST /api/approvals/{approvalId}/decision": "approvals.decide",
		"POST /api/approvals/{approvalId}/revoke":   "approvals.revoke",
	})
	registerDeclarations("approvals", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/approvals":              true,
		"GET /api/approvals/{approvalId}": true,
	})
}
