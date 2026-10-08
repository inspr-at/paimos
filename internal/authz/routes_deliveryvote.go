// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for deliveryvote.
func init() {
	registerRoutes("deliveryvote", map[string]string{
		"DELETE /api/harness-sessions/{sessionId}/delivery-rating": "nodes.read",
		"GET /api/harness-sessions/{sessionId}/delivery-rating":    "nodes.read",
		"GET /api/nodes/{nodeId}/delivery-ratings":                 "nodes.read",
		"PUT /api/harness-sessions/{sessionId}/delivery-rating":    "nodes.read",
	})
}
