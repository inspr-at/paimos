// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for crossreview.
func init() {
	registerRoutes("crossreview", map[string]string{
		"GET /api/nodes/{nodeId}/reviews":  "work_orders.read",
		"POST /api/nodes/{nodeId}/reviews": "work_orders.write",
		"POST /api/reviews/github":         "public", // Authenticated by host-owned GitHub HMAC, never a tenant session.
	})
}
