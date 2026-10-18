// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/hours.
func init() {
	registerRoutes("business_hours", map[string]string{
		"DELETE /api/time-entries/{id}":             "hours.write",
		"GET /api/nodes/{nodeId}/time-totals":       "hours.read",
		"GET /api/time-entries":                     "hours.read",
		"GET /api/time-periods":                     "hours.read",
		"GET /api/time-periods/{periodId}":          "hours.read",
		"PATCH /api/time-entries/{id}":              "hours.write",
		"POST /api/time-entries":                    "hours.write",
		"POST /api/time-periods":                    "hours.write",
		"POST /api/time-periods/{periodId}/approve": "hours.approve",
	})
}
