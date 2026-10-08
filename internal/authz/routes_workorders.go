// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for workorders.
func init() {
	registerRoutes("workorders", map[string]string{
		"GET /api/work-orders":                                             "work_orders.read",
		"GET /api/work-orders/{workOrderId}":                               "work_orders.read",
		"PATCH /api/work-orders/{workOrderId}":                             "work_orders.write",
		"POST /api/work-orders":                                            "work_orders.write",
		"POST /api/work-orders/{workOrderId}/criteria/{criterionId}/check": "work_orders.write",
		"POST /api/work-orders/{workOrderId}/evidence":                     "work_orders.write",
		"POST /api/work-orders/{workOrderId}/runs":                         "run.create", // Account choice only constrains routing; reservation/claim retain their scopes.
	})
}
