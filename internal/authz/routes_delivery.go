// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for delivery.
func init() {
	registerRoutes("delivery", map[string]string{
		"DELETE /api/delivery/{itemId}/hold":                                 "delivery.manage",
		"GET /api/delivery":                                                  "delivery.read",
		"GET /api/delivery/alerts":                                           "delivery.read",
		"GET /api/delivery/audit":                                            "delivery.read",
		"GET /api/delivery/enqueue-allowed":                                  "delivery.read",
		"GET /api/nodes/{id}/delivery":                                       "delivery.read",
		"GET /api/projects/{projectId}/delivery-queue":                       "delivery_queue.read",
		"GET /api/projects/{projectId}/delivery-queue/settings":              "delivery_queue.read",
		"GET /api/projects/{projectId}/delivery-reviews":                     "delivery_reviews.read",
		"GET /api/projects/{projectId}/delivery-reviews/settings":            "delivery_reviews.read",
		"GET /api/projects/{projectId}/delivery-settings":                    "delivery.read",
		"GET /api/projects/{projectId}/delivery-shipping":                    "delivery_ship.read",
		"GET /api/projects/{projectId}/delivery-shipping/settings":           "delivery_ship.read",
		"GET /api/projects/{projectId}/delivery/flow":                        "delivery.read",
		"GET /api/projects/{projectId}/delivery/flow/runs/{itemId}":          "delivery.read",
		"GET /api/projects/{projectId}/delivery/flow/stream":                 "delivery.read",
		"GET /api/projects/{projectId}/delivery/metrics":                     "delivery.read",
		"GET /api/projects/{projectId}/routing-decisions/{roundId}":          "delivery.route",
		"GET /api/projects/{projectId}/routing-settings":                     "delivery.read",
		"GET /api/settings/delivery":                                         "delivery.read",
		"PATCH /api/projects/{projectId}/delivery-queue/{roundId}":           "delivery_queue.manage",
		"POST /api/delivery/{itemId}/hold":                                   "delivery.manage",
		"POST /api/github/webhook":                                           "public",
		"POST /api/projects/{projectId}/delivery-queue":                      "delivery_queue.manage",
		"POST /api/projects/{projectId}/delivery-queue/claim":                "delivery_queue.claim",
		"POST /api/projects/{projectId}/delivery-queue/{roundId}/progress":   "delivery_queue.claim",
		"POST /api/projects/{projectId}/delivery-reviews":                    "delivery_reviews.manage",
		"POST /api/projects/{projectId}/delivery-reviews/{reviewId}/claim":   "delivery_reviews.claim",
		"POST /api/projects/{projectId}/delivery-reviews/{reviewId}/verdict": "delivery_reviews.report",
		"POST /api/projects/{projectId}/delivery-shipping/claim":             "delivery_ship.claim",
		"POST /api/projects/{projectId}/delivery/flow/rollout":               "delivery.manage",
		"POST /api/projects/{projectId}/delivery/metrics/backfill":           "delivery.manage",
		"POST /api/projects/{projectId}/delivery/metrics/facts":              "delivery.manage",
		"POST /api/projects/{projectId}/routing-decisions":                   "delivery.route",
		"PUT /api/projects/{projectId}/delivery-queue/settings":              "delivery_queue.manage",
		"PUT /api/projects/{projectId}/delivery-reviews/settings":            "delivery_reviews.manage",
		"PUT /api/projects/{projectId}/delivery-settings":                    "delivery.manage",
		"PUT /api/projects/{projectId}/delivery-shipping/settings":           "delivery_ship.manage",
		"PUT /api/projects/{projectId}/delivery/metrics/source":              "delivery.manage",
		"PUT /api/projects/{projectId}/routing-settings":                     "delivery.manage",
		"PUT /api/settings/delivery":                                         "delivery.manage",
	})
	registerDeclarations("delivery", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"DELETE /api/delivery/{itemId}/hold": true,
		"GET /api/delivery":                  true,
		"GET /api/delivery/alerts":           true,
		"GET /api/delivery/enqueue-allowed":  true,
		"GET /api/nodes/{id}/delivery":       true,
		"POST /api/delivery/{itemId}/hold":   true,
	})
}
