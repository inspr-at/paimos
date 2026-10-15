// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route declarations for crossreview. The visual-approval permission lives in
// permissions_crossreview.go so the Policies registry parse can see it.
func init() {
	registerDeclarations("crossreview", "project_filtered", ProjectFilteredRoutes, map[string]bool{"POST /api/review-runs/{runId}/credential": true})
	registerRoutes("crossreview", map[string]string{
		"POST /api/nodes/{nodeId}/review-runs":     "nodes.read",
		"GET /api/nodes/{nodeId}/review-evidence":  "nodes.read",
		"POST /api/review-runs/{runId}/credential": "nodes.read",
		"POST /api/review-runs/{runId}/verdict":    "public", // Authenticated only by its single-use run capability.
		"GET /api/nodes/{nodeId}/reviews":          "work_orders.read",
		"POST /api/nodes/{nodeId}/reviews":         "work_orders.write",
		"POST /api/reviews/github":                 "public", // Authenticated by host-owned GitHub HMAC, never a tenant session.
	})
}
